package phone

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Pairing: the owner opens a window in Settings → Phone, which shows an
// 8-digit code (5 minutes, one use); five wrong codes close it. A right
// code issues the phone a 32-byte random token, of which only the
// SHA-256 is kept.

const (
	devicesFile   = "devices.json"
	pairValidFor  = 5 * time.Minute
	pairMaxWrong  = 5
	codeDigits    = 8
	tokenBytes    = 32
	maxDeviceName = 64
	// seenEvery: a device's last-seen time is written at most this often.
	seenEvery = 10 * time.Minute
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

// Pairing is an open pairing window, as Settings shows it.
type Pairing struct {
	Code        string    `json:"code"`
	Expires     time.Time `json:"expires"`
	Fingerprint string    `json:"fingerprint"` // first 16 hex digits, grouped
	Addresses   []string  `json:"addresses"`
	Port        int       `json:"port"`
}

// devices is the paired phones, kept in devices.json (mode 0600).
type devices struct {
	mu     sync.Mutex
	path   string
	list   []Device
	saved  map[string]time.Time // last-seen as last written
	window *pairWindow
}

type pairWindow struct {
	code    string
	expires time.Time
	wrong   int
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

// open starts a pairing window, replacing any open one.
func (d *devices) open(now time.Time) (code string, expires time.Time, err error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", time.Time{}, err
	}
	code = fmt.Sprintf("%0*d", codeDigits, n.Int64())
	d.mu.Lock()
	defer d.mu.Unlock()
	d.window = &pairWindow{code: code, expires: now.Add(pairValidFor)}
	return code, d.window.expires, nil
}

// close ends the pairing window.
func (d *devices) close() {
	d.mu.Lock()
	d.window = nil
	d.mu.Unlock()
}

// windowOpen reports whether a pairing window is open, and until when.
func (d *devices) windowOpen(now time.Time) (bool, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.window == nil || !now.Before(d.window.expires) {
		return false, time.Time{}
	}
	return true, d.window.expires
}

var (
	errPairClosed = errors.New("no pairing window open")
	errPairWrong  = errors.New("wrong code")
	errPairLocked = errors.New("too many wrong codes; pairing window closed")
)

// pair checks code against the open window and, if right, adds a device
// and returns it with its token. The window closes on success, on
// expiry and after pairMaxWrong wrong codes.
func (d *devices) pair(code, name string, now time.Time) (Device, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.window
	if w == nil {
		return Device{}, "", errPairClosed
	}
	if !now.Before(w.expires) {
		d.window = nil
		return Device{}, "", errPairClosed
	}
	// Constant time over equal lengths; a code of another shape is just
	// a wrong one.
	if len(code) != len(w.code) || subtle.ConstantTimeCompare([]byte(code), []byte(w.code)) != 1 {
		w.wrong++
		if w.wrong >= pairMaxWrong {
			d.window = nil
			return Device{}, "", errPairLocked
		}
		return Device{}, "", errPairWrong
	}
	d.window = nil
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return Device{}, "", err
	}
	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return Device{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	dev := Device{ID: hex.EncodeToString(idb), Name: cleanName(name), Paired: now, LastSeen: now, TokenHash: hex.EncodeToString(sum[:])}
	d.list = append(d.list, dev)
	if err := d.saveLocked(); err != nil {
		d.list = d.list[:len(d.list)-1]
		return Device{}, "", err
	}
	dev.TokenHash = ""
	return dev, token, nil
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
