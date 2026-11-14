package phone

import (
	"net/netip"
	"strings"
	"sync"
	"time"
)

// privatePrefixes are the networks the Mac dials a phone on: RFC 1918,
// 100.64.0.0/10 and fd7a:115c:a1e0::/48 (Tailscale's), link-local and
// loopback. Nothing else is ever dialed, whatever an advertisement or a
// phone's reported addresses say.
var privatePrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		"fd7a:115c:a1e0::/48", "169.254.0.0/16", "127.0.0.0/8", "fe80::/10", "::1/128"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PrivateAddr reports whether ip is on one of those networks.
func PrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range privatePrefixes {
		if p.Contains(ip.WithZone("")) {
			return true
		}
	}
	return false
}

// dialable reports whether the Mac may dial ip: a private address, and an
// IPv6 link-local one only with its interface (zone), without which it
// can't be reached.
func dialable(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !PrivateAddr(ip) {
		return false
	}
	if ip.Is6() && ip.IsLinkLocalUnicast() {
		return ip.Zone() != ""
	}
	return true
}

// parseDialAddr reads an address as dns-sd prints it or a phone reports
// it ("192.168.1.20", "FE80:…%en0"): only a dialable one. A zone is kept
// for link-local IPv6 only (dns-sd prints "%<0>" on others).
func parseDialAddr(s string) (netip.Addr, bool) {
	if len(s) > 64 {
		return netip.Addr{}, false
	}
	host, zone, _ := strings.Cut(s, "%")
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if ip.Is6() && ip.IsLinkLocalUnicast() && validZone(zone) {
		ip = ip.WithZone(zone)
	}
	return ip, dialable(ip)
}

// validZone: an interface name, as macOS names them.
func validZone(z string) bool {
	if z == "" || len(z) > 16 {
		return false
	}
	for _, c := range z {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// reportedAddrs keeps, from what a phone's hello reports, the addresses
// worth dialing later (no link-local: the phone's interface name means
// nothing here; no loopback, which is this Mac), at most maxAddrs, in order, without duplicates.
func reportedAddrs(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		if len(out) >= maxAddrs {
			break
		}
		ip, ok := parseDialAddr(s)
		if !ok || ip.IsLinkLocalUnicast() || ip.IsLoopback() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	return out
}

const maxAddrs = 8

// everyMinute is a logger that says something at most once a minute, so
// a flapping phone can't flood the log.
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
