//go:build musubi_e2e

package phone

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"net/netip"
)

// Hooks for end-to-end tests in other packages (internal/gui's Explore
// test, built with -tags musubi_e2e): dialing a phone directly, as a
// sighting would, since an emulator can't be found over Bonjour. Not in
// any app build.

// E2EFingerprint reads a listening phone's certificate fingerprint (first
// 16 hex digits), as its advertisement would carry it.
func (s *Syncer) E2EFingerprint(ap netip.AddrPort) (string, error) {
	var fp string
	c, err := tls.Dial("tcp", ap.String(), s.tlsConfig(func(sum []byte) error { fp = hex.EncodeToString(sum); return nil }))
	if err != nil {
		return "", err
	}
	c.Close()
	return fp[:16], nil
}

// E2EDial dials the phone fp16 at ap: to pair (pair), or, when paired, to
// sync and, with live, open Explore. It returns at once.
func (s *Syncer) E2EDial(ctx context.Context, fp16 string, ap netip.AddrPort, pair, live bool) bool {
	t := target{fp16: fp16, pair: pair, live: live, addrs: []netip.AddrPort{ap}}
	if !pair {
		d, ok := s.pins.byPrefix(fp16)
		if !ok {
			return false
		}
		t.pin = d.ID
	}
	return s.start(ctx, t)
}

// E2EWait waits for every connection to end.
func (s *Syncer) E2EWait() { s.wg.Wait() }
