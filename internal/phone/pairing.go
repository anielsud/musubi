package phone

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Pairing is confirmed on both sides (docs/phone.md, "Pairing, confirmed
// on both"): the phone asks with a nonce, the Mac answers with its own,
// both show the same 6-digit code derived from the certificate and the two
// nonces, and the phone gets a token only once the owner has allowed it
// on the Mac and the phone has confirmed too. The token is 32 random
// bytes, of which only the SHA-256 is kept.

const (
	devicesFile = "devices.json"
	// pairValidFor: a request expires this long after it is made; once
	// both sides have confirmed, the phone has this long again to collect
	// its token.
	pairValidFor = 2 * time.Minute
	// At most pairMaxRequests requests per pairPer; pairMaxDenials denials
	// within pairPer refuse requests for pairLockFor.
	pairPer         = 10 * time.Minute
	pairMaxRequests = 5
	pairMaxDenials  = 3
	pairLockFor     = 10 * time.Minute
	// pairKeep: how long a finished request still answers the phone's poll.
	pairKeep      = 10 * time.Minute
	nonceBytes    = 16
	requestIDHex  = 32
	tokenBytes    = 32
	maxDeviceName = 64
	// seenEvery: a device's last-seen time is written at most this often.
	seenEvery = 10 * time.Minute
)

// Pair request states, as GET /v1/pair/{request_id} reports them.
const (
	PairPending = "pending"
	PairDenied  = "denied"
	PairExpired = "expired"
	PairPaired  = "paired"
)

// Device is a paired phone.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Paired   time.Time `json:"paired"`
	LastSeen time.Time `json:"last_seen,omitzero"`
	// TokenHash is the SHA-256 of the token, hex; never shown or logged.
	TokenHash string `json:"token_sha256"`
}

// PairPrompt is a pair request waiting for the owner's Allow or Deny, as
// the app shows it.
type PairPrompt struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"deviceName"`
	Code       string    `json:"code"`
	Expires    time.Time `json:"expires"`
}

// devices is the paired phones, kept in devices.json (mode 0600), and the
// pair requests in flight (memory only).
type devices struct {
	mu    sync.Mutex
	path  string
	list  []Device
	saved map[string]time.Time // last-seen as last written

	reqs     []*pairReq
	asked    []time.Time // when requests were accepted, within pairPer
	denied   []time.Time // when the owner denied, within pairPer
	lockedTo time.Time   // requests refused until then
}

type pairReq struct {
	id      string
	name    string
	code    string
	expires time.Time
	state   string // PairPending, PairDenied or PairExpired
	phoneOK bool
	macOK   bool
	// forget: when a finished request stops answering polls.
	forget time.Time
}

func openDevices(dir string) (*devices, error) {
	d := &devices{path: filepath.Join(dir, devicesFile), saved: map[string]time.Time{}}
	b, err := os.ReadFile(d.path)
	if errors.Is(err, fs.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Devices []Device `json:"devices"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("phone devices: %w", err)
	}
	d.list = f.Devices
	for _, x := range d.list {
		d.saved[x.ID] = x.LastSeen
	}
	return d, nil
}

func (d *devices) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(struct {
		Devices []Device `json:"devices"`
	}{d.list}, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivate(d.path, b); err != nil {
		return err
	}
	for _, x := range d.list {
		d.saved[x.ID] = x.LastSeen
	}
	return nil
}

// List is the paired devices without their token hashes.
func (d *devices) List() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Device, len(d.list))
	for i, x := range d.list {
		x.TokenHash = ""
		out[i] = x
	}
	return out
}

// PairCode is the 6-digit code both sides show: the first 4 bytes of
// SHA-256("musubi-pair-v1" ‖ certificate SHA-256 ‖ phone nonce ‖ Mac
// nonce), big-endian, mod 1,000,000, zero-padded. A machine in the middle
// shows the phone another certificate, so its code differs.
func PairCode(certSHA256, phoneNonce, macNonce []byte) string {
	h := sha256.New()
	h.Write([]byte("musubi-pair-v1"))
	h.Write(certSHA256)
	h.Write(phoneNonce)
	h.Write(macNonce)
	sum := h.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1_000_000)
}

var (
	errPairBusy     = errors.New("another pair request is pending")
	errPairLimited  = errors.New("too many pair requests")
	errPairLocked   = errors.New("pairing refused after denials")
	errPairNotFound = errors.New("no such pair request")
	errPairNotOpen  = errors.New("pair request no longer pending")
)

// sweepLocked expires what has run out and forgets what is old.
func (d *devices) sweepLocked(now time.Time) {
	keep := d.reqs[:0]
	for _, r := range d.reqs {
		if r.state == PairPending && !now.Before(r.expires) {
			r.state, r.forget = PairExpired, now.Add(pairKeep)
		}
		if r.state != PairPending && !now.Before(r.forget) {
			continue
		}
		keep = append(keep, r)
	}
	d.reqs = keep
	d.asked = within(d.asked, now)
	d.denied = within(d.denied, now)
}

// within drops the times older than pairPer.
func within(ts []time.Time, now time.Time) []time.Time {
	keep := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < pairPer {
			keep = append(keep, t)
		}
	}
	return keep
}

// request starts a pair request from a phone that sent phoneNonce: the
// Mac's nonce and the prompt the owner answers.
func (d *devices) request(name string, phoneNonce []byte, certSHA256 []byte, now time.Time) ([]byte, PairPrompt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	if now.Before(d.lockedTo) {
		return nil, PairPrompt{}, errPairLocked
	}
	for _, r := range d.reqs {
		if r.state == PairPending {
			return nil, PairPrompt{}, errPairBusy
		}
	}
	if len(d.asked) >= pairMaxRequests {
		return nil, PairPrompt{}, errPairLimited
	}
	macNonce := make([]byte, nonceBytes)
	idb := make([]byte, requestIDHex/2)
	if _, err := rand.Read(macNonce); err != nil {
		return nil, PairPrompt{}, err
	}
	if _, err := rand.Read(idb); err != nil {
		return nil, PairPrompt{}, err
	}
	r := &pairReq{id: hex.EncodeToString(idb), name: cleanName(name), code: PairCode(certSHA256, phoneNonce, macNonce),
		expires: now.Add(pairValidFor), state: PairPending}
	d.reqs = append(d.reqs, r)
	d.asked = append(d.asked, now)
	return macNonce, r.prompt(), nil
}

func (r *pairReq) prompt() PairPrompt {
	return PairPrompt{ID: r.id, DeviceName: r.name, Code: r.code, Expires: r.expires}
}

// findLocked is the request with this id. The id is what lets a phone
// collect its token, so every request's id is compared in constant time.
func (d *devices) findLocked(id string) *pairReq {
	var found *pairReq
	for _, r := range d.reqs {
		if len(id) == len(r.id) && subtle.ConstantTimeCompare([]byte(id), []byte(r.id)) == 1 {
			found = r
		}
	}
	return found
}

// prompt is the request waiting for the owner, if any.
func (d *devices) prompt(now time.Time) *PairPrompt {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	for _, r := range d.reqs {
		if r.state == PairPending && !r.macOK {
			p := r.prompt()
			return &p
		}
	}
	return nil
}

// phoneConfirm records the phone's Confirm.
func (d *devices) phoneConfirm(id string, now time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	r := d.findLocked(id)
	if r == nil {
		return errPairNotFound
	}
	if r.state != PairPending {
		return errPairNotOpen
	}
	if !r.phoneOK {
		r.phoneOK = true
		r.bothLocked(now)
	}
	return nil
}

// answer records the owner's Allow or Deny. A third denial within pairPer
// refuses requests for pairLockFor (locked is then true).
func (d *devices) answer(id string, allow bool, now time.Time) (name string, locked bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	r := d.findLocked(id)
	if r == nil {
		return "", false, errPairNotFound
	}
	if r.state != PairPending || r.macOK {
		return "", false, errPairNotOpen
	}
	if allow {
		r.macOK = true
		r.bothLocked(now)
		return r.name, false, nil
	}
	r.state, r.forget = PairDenied, now.Add(pairKeep)
	d.denied = append(d.denied, now)
	if len(d.denied) >= pairMaxDenials {
		d.lockedTo, d.denied = now.Add(pairLockFor), nil
		locked = true
	}
	return r.name, locked, nil
}

// bothLocked gives the phone pairValidFor from the second confirmation to
// collect its token.
func (r *pairReq) bothLocked(now time.Time) {
	if r.phoneOK && r.macOK {
		r.expires = now.Add(pairValidFor)
	}
}

// status is where request id stands. Once both sides have confirmed, it
// adds the device and returns it with its token, once: the request is
// then gone.
func (d *devices) status(id string, now time.Time) (string, Device, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked(now)
	r := d.findLocked(id)
	if r == nil {
		return "", Device{}, "", errPairNotFound
	}
	if r.state != PairPending || !r.phoneOK || !r.macOK {
		return r.state, Device{}, "", nil
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Device{}, "", err
	}
	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return "", Device{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	dev := Device{ID: hex.EncodeToString(idb), Name: r.name, Paired: now, LastSeen: now, TokenHash: hex.EncodeToString(sum[:])}
	d.list = append(d.list, dev)
	if err := d.saveLocked(); err != nil {
		d.list = d.list[:len(d.list)-1]
		return "", Device{}, "", err
	}
	for i, x := range d.reqs {
		if x == r {
			d.reqs = append(d.reqs[:i:i], d.reqs[i+1:]...)
			break
		}
	}
	dev.TokenHash = ""
	return PairPaired, dev, token, nil
}

// auth finds the device a bearer token belongs to. Every device's hash is
// compared, in constant time, so the time taken says nothing.
func (d *devices) auth(token string, now time.Time) (Device, bool) {
	if len(token) < 16 || len(token) > 512 {
		return Device{}, false
	}
	sum := sha256.Sum256([]byte(token))
	want := []byte(hex.EncodeToString(sum[:]))
	d.mu.Lock()
	defer d.mu.Unlock()
	found := -1
	for i := range d.list {
		if subtle.ConstantTimeCompare(want, []byte(d.list[i].TokenHash)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return Device{}, false
	}
	d.list[found].LastSeen = now
	if now.Sub(d.saved[d.list[found].ID]) >= seenEvery {
		_ = d.saveLocked() // a last-seen time isn't worth failing a request for
	}
	dev := d.list[found]
	dev.TokenHash = ""
	return dev, true
}

// revoke removes a device; its token stops working at once.
func (d *devices) revoke(id string) (Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.list {
		if x.ID == id {
			d.list = append(d.list[:i:i], d.list[i+1:]...)
			if err := d.saveLocked(); err != nil {
				d.list = append(d.list[:i], append([]Device{x}, d.list[i:]...)...)
				return Device{}, err
			}
			delete(d.saved, id)
			x.TokenHash = ""
			return x, nil
		}
	}
	return Device{}, fmt.Errorf("no such device")
}

// cleanName is a device name fit to show: printable, one line, at most
// maxDeviceName characters; "Phone" if nothing is left.
func cleanName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n >= maxDeviceName {
			break
		}
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	if out := strings.TrimSpace(b.String()); out != "" {
		return out
	}
	return "Phone"
}
