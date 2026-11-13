package phone

import (
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// While the server listens, the Mac advertises itself on the local network
// (Bonjour) so the phone finds it without a typed address (docs/phone.md,
// "Finding the Mac"): instance "Musubi on <computer name>", the port, and
// TXT v=1 and fp=<first 16 hex digits of the certificate fingerprint>.

// ServiceType is the Bonjour service the phone browses for.
const ServiceType = "_musubi._tcp"

// errAdvertiseUnsupported: this system has no advertiser (linux).
var errAdvertiseUnsupported = errors.New("not available on this system")

// advertiseFunc starts advertising; platformAdvertise on the real
// system, a fake in tests.
type advertiseFunc func(instance string, port int, txt []string) (advertProc, error)

// advertProc is a running advertisement.
type advertProc interface {
	// Stop ends it and waits until it has.
	Stop()
	// Done is closed when it has ended, by Stop or by itself.
	Done() <-chan struct{}
}

type advertState struct {
	mu       sync.Mutex
	proc     advertProc
	on       bool
	err      string
	instance string
}

// Advertising reports whether the Mac is being advertised, under which
// name, and why not when it should be.
func (s *Server) Advertising() (on bool, instance, why string) {
	s.ad.mu.Lock()
	defer s.ad.mu.Unlock()
	return s.ad.on, s.ad.instance, s.ad.err
}

func (s *Server) startAdvertising(port int) {
	instance := instanceName(computerName())
	proc, err := s.advertise(instance, port, []string{"v=1", "fp=" + s.fp[:16]})
	s.ad.mu.Lock()
	s.ad.instance = instance
	if err != nil {
		s.ad.on, s.ad.err = false, err.Error()
		s.ad.mu.Unlock()
		if !errors.Is(err, errAdvertiseUnsupported) {
			s.cfg.Logf("phone: not advertised on the local network: %v", err)
		}
		s.cfg.OnChange()
		return
	}
	s.ad.proc, s.ad.on, s.ad.err = proc, true, ""
	s.ad.mu.Unlock()
	s.cfg.Logf("phone: advertised on the local network as %q", instance)
	s.cfg.OnChange()
	go func() {
		<-proc.Done()
		s.ad.mu.Lock()
		ended := s.ad.proc == proc // not stopped by stopAdvertising
		if ended {
			s.ad.proc, s.ad.on, s.ad.err = nil, false, "the advertisement stopped"
		}
		s.ad.mu.Unlock()
		if ended {
			s.cfg.Logf("phone: the local-network advertisement stopped by itself")
			s.cfg.OnChange()
		}
	}()
}

func (s *Server) stopAdvertising() {
	s.ad.mu.Lock()
	proc := s.ad.proc
	s.ad.proc, s.ad.on, s.ad.err = nil, false, ""
	s.ad.mu.Unlock()
	if proc != nil {
		proc.Stop()
		s.cfg.Logf("phone: no longer advertised on the local network")
		s.cfg.OnChange()
	}
}

// instanceName is "Musubi on <computer>", printable, within Bonjour's 63
// bytes.
func instanceName(computer string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(computer) {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = "this Mac"
	}
	s := "Musubi on " + name
	for len(s) > 63 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return strings.TrimSpace(s)
}
