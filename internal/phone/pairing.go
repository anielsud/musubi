package phone

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"
)

// Pairing (docs/phone.md, "Pairing, confirmed on both"): the owner opens
// pairing on the phone, which then advertises pair=1; the Mac dials it,
// presents its certificate and sends pair_request with a nonce; the phone
// answers with its own. Both show a 6-digit code derived from both
// certificates (each side takes them from the TLS session) and both
// nonces. The owner allows it here, the Mac sends pair_confirm and waits
// for the phone's owner to confirm there; only then is the phone's
// certificate pinned. Deny sends pair_cancel.

const (
	pairValidFor = 2 * time.Minute
	// pairMaxDenials denials of the same phone certificate within pairPer
	// ignore that phone for pairIgnoreFor.
	pairPer        = 10 * time.Minute
	pairMaxDenials = 3
	pairIgnoreFor  = 10 * time.Minute
	// pairRetryAfter: a phone still advertising pair=1 after an attempt
	// ended is dialed again no sooner than this.
	pairRetryAfter = 30 * time.Second
	nonceBytes     = 16
)

// PairCode is the 6-digit code both sides show: the first 4 bytes of
// SHA-256("musubi-pair-v2" ‖ Mac certificate SHA-256 ‖ phone certificate
// SHA-256 ‖ Mac nonce ‖ phone nonce), big-endian, mod 1,000,000,
// zero-padded. A machine in the middle presents another certificate to
// at least one side, so the codes differ.
func PairCode(macCert, phoneCert, macNonce, phoneNonce []byte) string {
	h := sha256.New()
	h.Write([]byte("musubi-pair-v2"))
	h.Write(macCert)
	h.Write(phoneCert)
	h.Write(macNonce)
	h.Write(phoneNonce)
	sum := h.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1_000_000)
}

// PairPrompt is a pairing waiting for the owner's Allow or Deny, as the
// app shows it.
type PairPrompt struct {
	ID         string    `json:"id"`
	DeviceName string    `json:"deviceName"`
	Code       string    `json:"code"`
	Expires    time.Time `json:"expires"`
}

type openPrompt struct {
	PairPrompt
	answer chan bool
}

// decodeNonce reads a base64 nonce (standard or URL alphabet, padded or
// not) of exactly nonceBytes bytes.
func decodeNonce(v string) ([]byte, bool) {
	if len(v) > 32 {
		return nil, false
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil {
			return b, len(b) == nonceBytes
		}
	}
	return nil, false
}

// PairPrompt is the pairing waiting for the owner, or nil.
func (s *Syncer) PairPrompt() *PairPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prompt == nil || !s.cfg.Now().Before(s.prompt.Expires) {
		return nil
	}
	p := s.prompt.PairPrompt
	return &p
}

// AnswerPair is the owner's Allow (allow true) or Deny of the pairing.
func (s *Syncer) AnswerPair(id string, allow bool) error {
	s.mu.Lock()
	p := s.prompt
	if p == nil || len(id) != len(p.ID) || subtle.ConstantTimeCompare([]byte(id), []byte(p.ID)) != 1 {
		s.mu.Unlock()
		return errors.New("that pairing is no longer waiting")
	}
	s.prompt = nil
	s.mu.Unlock()
	select {
	case p.answer <- allow:
	default:
	}
	s.cfg.OnChange()
	return nil
}

// pairIgnored reports whether pairing with the phone whose certificate
// starts with fp16 is ignored after denials.
func (s *Syncer) pairIgnored(fp16 string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Now().Before(s.ignore[fp16])
}

// denied records a Deny; the third within pairPer ignores the phone.
func (s *Syncer) denied(fp string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	var keep []time.Time
	for _, t := range s.denials[fp] {
		if now.Sub(t) < pairPer {
			keep = append(keep, t)
		}
	}
	keep = append(keep, now)
	if len(keep) >= pairMaxDenials {
		s.ignore[fp[:16]] = now.Add(pairIgnoreFor)
		delete(s.denials, fp)
		return true
	}
	s.denials[fp] = keep
	return false
}

// pair runs a pairing on fc, whose phone presented phoneCert (its
// SHA-256). addr is where it was dialed. Its outcome is logged.
func (s *Syncer) pair(ctx context.Context, fc *frameConn, phoneCert []byte, addr net.Addr) {
	fp := hex.EncodeToString(phoneCert)
	if s.pairIgnored(fp[:16]) {
		return
	}
	macNonce := make([]byte, nonceBytes)
	if _, err := rand.Read(macNonce); err != nil {
		return
	}
	var rep struct {
		DeviceName string `json:"device_name"`
		Nonce      string `json:"nonce"`
	}
	err := fc.call(map[string]any{"op": "pair_request", "mac_name": s.cfg.MacName, "nonce": base64.StdEncoding.EncodeToString(macNonce)}, &rep)
	if err != nil {
		s.cfg.Logf("phone: pairing: no pair request: %v", err)
		return
	}
	phoneNonce, ok := decodeNonce(rep.Nonce)
	if !ok {
		s.cfg.Logf("phone: pairing: %v", protoErr("bad nonce"))
		return
	}
	name := cleanName(rep.DeviceName)
	idb := make([]byte, 16)
	if _, err := rand.Read(idb); err != nil {
		return
	}
	p := &openPrompt{PairPrompt: PairPrompt{ID: hex.EncodeToString(idb), DeviceName: name,
		Code: PairCode(s.fpRaw, phoneCert, macNonce, phoneNonce), Expires: s.cfg.Now().Add(s.pairWait)}, answer: make(chan bool, 1)}
	s.mu.Lock()
	s.prompt = p
	s.mu.Unlock()
	s.cfg.Logf("phone: %q asks to pair; asking the owner", name)
	s.cfg.OnPairRequest(p.PairPrompt)
	s.cfg.OnChange()
	defer func() {
		s.mu.Lock()
		if s.prompt == p {
			s.prompt = nil
		}
		s.mu.Unlock()
		s.cfg.OnChange()
	}()

	allow, why := s.awaitAnswer(ctx, fc, p)
	switch {
	case why != "":
		s.cfg.Logf("phone: pairing with %q ended: %s", name, why)
		if why == "not answered in time" {
			fc.call(map[string]any{"op": "pair_cancel"}, nil)
		}
	case !allow:
		fc.call(map[string]any{"op": "pair_cancel"}, nil)
		s.cfg.Logf("phone: pairing with %q denied", name)
		if s.denied(fp) {
			s.cfg.Logf("phone: %d pairings denied in %s; ignoring that phone for %s", pairMaxDenials, pairPer, pairIgnoreFor)
		}
	default:
		if err := fc.send(map[string]any{"op": "pair_confirm"}, nil, 0); err != nil {
			s.cfg.Logf("phone: pairing with %q: %v", name, err)
			return
		}
		_, err := fc.recv(confirmWait, nil, false)
		var pe *phoneError
		switch {
		case errors.As(err, &pe):
			s.cfg.Logf("phone: pairing with %q not confirmed on the phone (%s)", name, pe.code)
		case err != nil:
			s.cfg.Logf("phone: pairing with %q: %v", name, err)
		default:
			now := s.cfg.Now()
			d := Device{ID: fp, Name: name, Paired: now, Addresses: reportedAddrs([]string{hostOf(addr)})}
			if err := s.pins.pin(d); err != nil {
				s.cfg.Logf("phone: pairing with %q: not saved: %v", name, err)
				return
			}
			s.cfg.Logf("phone: paired with %q (%s)", name, fp[:16])
		}
	}
}

// awaitAnswer waits for the owner's answer to p while watching the
// connection: the phone sends nothing until asked, so anything it sends,
// or its closing, ends the pairing (why says how).
func (s *Syncer) awaitAnswer(ctx context.Context, fc *frameConn, p *openPrompt) (allow bool, why string) {
	watched := make(chan error, 1)
	fc.c.SetReadDeadline(time.Time{})
	go func() {
		_, err := fc.br.Peek(1)
		if err == nil {
			err = protoErr("the phone spoke out of turn")
		}
		watched <- err
	}()
	stopWatch := func() bool {
		fc.c.SetReadDeadline(time.Now())
		err := <-watched
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}
	timer := time.NewTimer(s.pairWait)
	defer timer.Stop()
	select {
	case allow = <-p.answer:
		if !stopWatch() {
			return false, "the phone closed the connection"
		}
		return allow, ""
	case <-timer.C:
		if !stopWatch() {
			return false, "the phone closed the connection"
		}
		return false, "not answered in time"
	case <-watched:
		return false, "the phone cancelled"
	case <-ctx.Done():
		stopWatch()
		return false, "phone sync stopped"
	}
}

func hostOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return ""
	}
	return host
}
