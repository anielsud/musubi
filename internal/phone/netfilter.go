package phone

import (
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// privatePrefixes are the networks a phone may connect from: RFC 1918,
// 100.64.0.0/10 (Tailscale's), link-local and loopback. Anything else is
// closed at accept, before TLS.
var privatePrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"169.254.0.0/16", "127.0.0.0/8", "fe80::/10", "::1/128"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PrivateAddr reports whether ip is on one of the networks a phone may
// connect from.
func PrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range privatePrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteAllowed reports whether a connection's peer is private.
func remoteAllowed(a net.Addr) bool {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return false
	}
	return PrivateAddr(ap.Addr())
}

// privateListener closes every connection from outside the private
// networks as it is accepted.
type privateListener struct {
	net.Listener
	refused func(net.Addr)
}

func (l privateListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if remoteAllowed(c.RemoteAddr()) {
			return c, nil
		}
		c.Close()
		if l.refused != nil {
			l.refused(c.RemoteAddr())
		}
	}
}

// everyMinute is a logger that says something at most once a minute, so
// a scan or a stuck client can't flood the log.
type everyMinute struct {
	mu   sync.Mutex
	last time.Time
}

func (e *everyMinute) ok(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now.Sub(e.last) < time.Minute {
		return false
	}
	e.last = now
	return true
}

// LocalAddresses are this Mac's addresses a phone can reach it on
// (private, not loopback or IPv6 link-local, which needs a zone), IPv4
// first, for the pairing screen.
func LocalAddresses() []string {
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range ifaces {
		pfx, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		ip := pfx.Addr().Unmap()
		if ip.IsLoopback() || (ip.Is6() && ip.IsLinkLocalUnicast()) || !PrivateAddr(ip) {
			continue
		}
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Is4() != out[j].Is4() {
			return out[i].Is4()
		}
		return out[i].Less(out[j])
	})
	s := make([]string, len(out))
	for i, ip := range out {
		s[i] = ip.String()
	}
	return s
}
