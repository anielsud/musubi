// Package phone is the Musubi app's side of phone sync (docs/phone.md,
// part 2). The Mac makes only outgoing connections (its firewall refuses
// incoming ones to apps without an Apple-issued signature): it finds
// phones on the local network over Bonjour, dials the paired ones and
// those asking to pair, and over mutual TLS fetches the meetings they
// recorded, has the recorder import them, reports back where each stands
// and pushes the owner's agenda with its briefs. It is off unless
// [phone] enabled is set.
package phone

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

const (
	// ServiceType is what a phone advertises while it accepts syncs.
	ServiceType = "_musubi-phone._tcp"
	// PhonePort is the port a phone listens on, dialed at the addresses
	// it last reported.
	PhonePort = 47616

	dialTimeout      = 10 * time.Second
	handshakeTimeout = 15 * time.Second
	// knownEvery: a paired phone not seen on the local network for this
	// long is dialed at its last reported addresses (Tailscale).
	knownEvery = 10 * time.Minute
	// resyncVisible: a paired phone that stays advertised is synced again
	// this often; resyncGap is the least time between two syncs a new
	// sighting starts.
	resyncVisible = 2 * time.Minute
	resyncGap     = 20 * time.Second
)

// Config is what the syncer needs from the app.
type Config struct {
	// Dir is phone sync's folder in the Musubi data folder: certificate,
	// paired phones, staged recordings.
	Dir string
	// RecorderData is the recorder's data folder, whose inbox the joined
	// audio is written to.
	RecorderData string
	// MacName is shown on the phone; "" is the computer's name.
	MacName string
	// Import asks the recorder to import (control.OpImport); an error
	// wrapping control.ErrNotRunning leaves the recording waiting.
	Import func(ctx context.Context, args control.ImportArgs) (control.ImportStatus, error)
	// Agenda lists the events in the next days days; Drafting starts the
	// briefs missing up to until drafting in the background (unless a
	// meeting is live) and says how many are left.
	Agenda func(ctx context.Context, days int) ([]Event, error)
	// Details applies a recording's notes, people and photos to its
	// session folder ([phone] details); OnDetails is told when it did.
	Details   bool
	OnDetails func()
	Drafting  func(until time.Time) int
	// Logf logs (never a nonce, a code or a key); OnChange tells Settings
	// that the phones, a pairing or the browsing changed; OnPairRequest
	// asks the owner to Allow or Deny a pairing.
	Logf          func(format string, args ...any)
	OnChange      func()
	OnPairRequest func(PairPrompt)
	Now           func() time.Time
	// Live serves Explore on the phone (docs/phone.md, part 3) from
	// Assets, the app's built frontend; nil: a phone advertising live=1
	// is only synced, as before.
	Live   LiveHandler
	Assets fs.FS
}

// Syncer is phone sync. Its paired phones can be listed and unpaired
// whether or not it runs.
type Syncer struct {
	cfg  Config
	cert tls.Certificate
	// fpRaw is the certificate's SHA-256, the pairing code's input.
	fpRaw []byte
	pins  *pins

	// dnssd runs the system's dns-sd (a fake in tests); dial opens TCP
	// connections; pairWait is how long a pairing waits for the owner.
	dnssd    dnssdFunc
	dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	pairWait time.Duration
	// phonePort is dialed at reported addresses: PhonePort.
	phonePort uint16
	// Live mode's timings (live.go), shortened in tests.
	liveIdle, livePing, liveReadWait, liveCallTimeout time.Duration

	mu      sync.Mutex
	looking bool
	lookErr string
	// busy: phones (by the first 16 hex digits of their certificate
	// SHA-256) with a connection open; pairing: one is under way.
	busy    map[string]bool
	pairing bool
	prompt  *openPrompt
	denials map[string][]time.Time
	ignore  map[string]time.Time
	// pairTried, seen, synced: when a pairing with, a sighting of and a
	// sync with each phone last happened.
	pairTried map[string]time.Time
	seen      map[string]time.Time
	synced    map[string]time.Time
	// liveIdled: phones whose live connection was closed for being idle;
	// dialed for live mode again only once their advertisement changes.
	liveIdled map[string]bool

	recMu sync.Mutex
	recs  map[string]*sync.Mutex

	unreachableLog everyMinute
	wg             sync.WaitGroup
}

// New opens phone sync's folder, making the certificate on first use.
func New(cfg Config) (*Syncer, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	if cfg.OnPairRequest == nil {
		cfg.OnPairRequest = func(PairPrompt) {}
	}
	if cfg.MacName == "" {
		cfg.MacName = cleanText(computerName(), maxDeviceName)
	}
	if cfg.MacName == "" {
		cfg.MacName = "Mac"
	}
	if err := os.MkdirAll(filepath.Join(cfg.Dir, "recordings"), 0o700); err != nil {
		return nil, err
	}
	cert, err := loadOrCreateCert(cfg.Dir, cfg.Now())
	if err != nil {
		return nil, err
	}
	p, err := openPins(cfg.Dir)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(cert.Certificate[0])
	var d net.Dialer
	return &Syncer{cfg: cfg, cert: cert, fpRaw: sum[:], pins: p, dnssd: platformDNSSD, dial: d.DialContext, pairWait: pairValidFor, phonePort: PhonePort,
		liveIdle: liveIdleClose, livePing: livePingEvery, liveReadWait: liveReadWait, liveCallTimeout: liveCallTimeout,
		busy: map[string]bool{}, denials: map[string][]time.Time{}, ignore: map[string]time.Time{},
		pairTried: map[string]time.Time{}, seen: map[string]time.Time{}, synced: map[string]time.Time{}, liveIdled: map[string]bool{},
		recs: map[string]*sync.Mutex{}}, nil
}

// Fingerprint is the Mac certificate's SHA-256 (hex).
func (s *Syncer) Fingerprint() string { return hex.EncodeToString(s.fpRaw) }

// Devices lists the paired phones.
func (s *Syncer) Devices() []Device { return s.pins.List() }

// Unpair removes a phone's pin: its next connection fails.
func (s *Syncer) Unpair(id string) error {
	d, err := s.pins.remove(id)
	if err != nil {
		return err
	}
	s.cfg.Logf("phone: unpaired %q", d.Name)
	s.cfg.OnChange()
	return nil
}

// Looking reports whether the Mac is looking for phones on the local
// network, and why not when it should be.
func (s *Syncer) Looking() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.looking, s.lookErr
}

func (s *Syncer) setLooking(on bool, why string) {
	s.mu.Lock()
	changed := s.looking != on || s.lookErr != why
	s.looking, s.lookErr = on, why
	s.mu.Unlock()
	if changed {
		s.cfg.OnChange()
	}
}

// Run looks for phones and syncs them until ctx ends; it returns once
// every dns-sd it started and every connection has ended.
func (s *Syncer) Run(ctx context.Context) {
	s.cfg.Logf("phone: sync on; looking for phones")
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.browse(ctx)
	}()
	// The first dial of known addresses waits a minute, so phones on the
	// local network are found there first.
	t := time.NewTimer(time.Minute)
	defer t.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-t.C:
			s.dialKnown(ctx)
			t.Reset(knownEvery)
		}
	}
	s.wg.Wait()
	s.setLooking(false, "")
	s.cfg.Logf("phone: sync off")
}

// target is a phone to dial: fp16 is its certificate's first 16 hex
// digits (advertised, or its pin's), pin its pinned certificate unless
// pairing; live: it asks for Explore (live=1).
type target struct {
	fp16  string
	pin   string
	pair  bool
	live  bool
	addrs []netip.AddrPort
	// resyncs: syncs this dial has run because the phone ended Explore to
	// sync (live mode); the next one is run whether or not it is due.
	resyncs int
}

// start dials t in the background unless that phone already has a
// connection (or, to pair, a pairing is under way).
func (s *Syncer) start(ctx context.Context, t target) bool {
	s.mu.Lock()
	if s.busy[t.fp16] || (t.pair && s.pairing) {
		s.mu.Unlock()
		return false
	}
	s.busy[t.fp16] = true
	if t.pair {
		s.pairing = true
	}
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.busy, t.fp16)
			if t.pair {
				s.pairing = false
				s.pairTried[t.fp16] = s.cfg.Now()
			}
			s.mu.Unlock()
		}()
		used, fp := s.session(ctx, t)
		if t.pair && fp != "" && ctx.Err() == nil {
			// Paired: sync at once, on a connection of its own.
			if _, ok := s.pins.get(fp); ok {
				s.session(ctx, target{fp16: t.fp16, pin: fp, addrs: []netip.AddrPort{used}})
			}
		}
	}()
	return true
}

var errWrongCert = errors.New("the phone presented another certificate")

// tlsConfig is the client side of the mutual TLS: the Mac's certificate,
// and the phone's checked by check (its SHA-256), not by any CA.
func (s *Syncer) tlsConfig(check func(sum []byte) error) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// TLS 1.3's suites are all AEAD; these are 1.2's ECDHE + AEAD.
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
		},
		// Always presented: the phone names no CA, and it trusts the Mac
		// by its pin.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &s.cert, nil },
		// No CA and no hostname: the phone's certificate is self-signed
		// and trusted only by its pinned SHA-256, checked in
		// VerifyConnection. Removing that check removes all trust.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errWrongCert
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			return check(sum[:])
		},
	}
}

// dialTLS opens a mutual-TLS connection to ap for t, returning the
// phone's certificate SHA-256.
func (s *Syncer) dialTLS(ctx context.Context, ap netip.AddrPort, t target) (*tls.Conn, []byte, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	raw, err := s.dial(dctx, "tcp", ap.String())
	cancel()
	if err != nil {
		return nil, nil, err
	}
	var got []byte
	cfg := s.tlsConfig(func(sum []byte) error {
		if t.pair {
			// The certificate must be the one the phone advertised.
			if hex.EncodeToString(sum)[:16] != t.fp16 {
				return errWrongCert
			}
		} else if want, err := hex.DecodeString(t.pin); err != nil || subtle.ConstantTimeCompare(sum, want) != 1 {
			return errWrongCert
		}
		got = append([]byte{}, sum...)
		return nil
	})
	tc := tls.Client(raw, cfg)
	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		raw.Close()
		return nil, nil, err
	}
	return tc, got, nil
}

// session dials t's addresses in turn and pairs or syncs on the first
// that answers. It returns the address used and, after a pairing, the
// phone's certificate SHA-256.
func (s *Syncer) session(ctx context.Context, t target) (netip.AddrPort, string) {
	var conn *tls.Conn
	var sum []byte
	var used netip.AddrPort
	var errs []error
	for _, ap := range t.addrs {
		c, got, err := s.dialTLS(ctx, ap, t)
		if err == nil {
			conn, sum, used = c, got, ap
			break
		}
		errs = append(errs, err)
		if errors.Is(err, errWrongCert) {
			break
		}
	}
	// Every address's error, not just the last: an IPv6 link-local
	// "no route" hid why the phone's LAN address failed.
	lastErr := errors.Join(errs...)
	if conn == nil {
		s.dialFailed(t, lastErr)
		return used, ""
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	fc := newFrameConn(conn)
	if t.pair {
		s.pair(ctx, fc, sum, conn.RemoteAddr())
		return used, hex.EncodeToString(sum)
	}
	if !t.live {
		s.sync(ctx, fc, t.pin, false)
		return used, ""
	}
	// Explore: a sync first when one is due, then live mode on the same
	// connection (the phone serves one connection at a time).
	s.mu.Lock()
	last := s.synced[t.fp16]
	s.mu.Unlock()
	if t.resyncs > 0 || last.IsZero() || s.cfg.Now().Sub(last) >= resyncGap {
		if !s.sync(ctx, fc, t.pin, true) {
			return used, ""
		}
	}
	name := "phone"
	if d, ok := s.pins.get(t.pin); ok {
		name = d.Name
	}
	switch s.startLive(ctx, fc, name) {
	case liveEndIdle:
		s.mu.Lock()
		s.liveIdled[t.fp16] = true
		s.mu.Unlock()
	case liveEndSync:
		// The phone ended Explore to sync (a recording finished): sync on
		// a fresh connection, then Explore again; it still advertises
		// live=1.
		if t.resyncs < maxLiveResyncs && ctx.Err() == nil {
			conn.Close()
			next := t
			next.resyncs++
			next.addrs = append([]netip.AddrPort{used}, without(t.addrs, used)...)
			return s.session(ctx, next)
		}
	}
	return used, ""
}

func without(aps []netip.AddrPort, x netip.AddrPort) []netip.AddrPort {
	var out []netip.AddrPort
	for _, ap := range aps {
		if ap != x {
			out = append(out, ap)
		}
	}
	return out
}

// dialFailed logs a phone that couldn't be reached (at most once a
// minute) or refused (always: the pin didn't match).
func (s *Syncer) dialFailed(t target, err error) {
	if err == nil {
		return
	}
	name := "a phone asking to pair"
	if d, ok := s.pins.get(t.pin); ok {
		name = d.Name
	}
	if errors.Is(err, errWrongCert) {
		s.cfg.Logf("phone: %q presented a certificate other than the paired one; not synced (pair again if the phone was reset)", name)
		if t.pin != "" {
			s.pins.update(t.pin, func(d *Device) { d.LastOutcome = "refused: not the paired phone's certificate" })
			s.cfg.OnChange()
		}
		return
	}
	if t.pin != "" && refusedByPhone(err) {
		s.phoneRefused(t.pin, name, err)
		return
	}
	if s.unreachableLog.ok(s.cfg.Now()) {
		s.cfg.Logf("phone: %q not reachable: %v", name, err)
	}
}

// dialKnown dials each paired phone not seen on the local network lately
// at the addresses it last reported.
func (s *Syncer) dialKnown(ctx context.Context) {
	now := s.cfg.Now()
	for _, d := range s.pins.List() {
		fp16 := d.ID[:16]
		s.mu.Lock()
		seen := s.seen[fp16]
		s.mu.Unlock()
		if !seen.IsZero() && now.Sub(seen) < knownEvery {
			continue
		}
		var addrs []netip.AddrPort
		for _, a := range d.Addresses {
			if ip, ok := parseDialAddr(a); ok {
				addrs = append(addrs, netip.AddrPortFrom(ip, s.phonePort))
			}
		}
		if len(addrs) > 0 {
			s.start(ctx, target{fp16: fp16, pin: d.ID, addrs: addrs})
		}
	}
}
