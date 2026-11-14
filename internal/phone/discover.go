package phone

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Finding phones (docs/phone.md, "Finding phones"): while sync is on, the
// Mac browses _musubi-phone._tcp with the system's dns-sd, follows each
// phone it sees (dns-sd -L: host, port and TXT, again whenever they
// change), and dials a paired one (its advertised fp is a pin's first 16
// hex digits; the handshake then checks the whole pin) or one advertising
// pair=1, at the private addresses its host resolves to (dns-sd -G).

const (
	// maxFollowed: phones followed at once (one dns-sd -L each).
	maxFollowed = 16
	// resolveWait bounds an address lookup; resolveSettle is how long it
	// waits for more addresses after the first.
	resolveWait   = 3 * time.Second
	resolveSettle = 300 * time.Millisecond
	maxLine       = 4 << 10
)

// errDNSSDUnsupported: this system has no dns-sd (linux).
var errDNSSDUnsupported = errors.New("Bonjour isn't available on this system")

// dnssdFunc starts dns-sd with args; platformDNSSD on the real system, a
// fake in tests.
type dnssdFunc func(args ...string) (dnssdProc, error)

// dnssdProc is a running dns-sd.
type dnssdProc interface {
	// Lines is its output, line by line, closed when it ends.
	Lines() <-chan string
	// Stop ends it (and its wrapper) and waits until it has.
	Stop()
}

var (
	instancePattern = regexp.MustCompile(`^musubi-[0-9a-f]{8}$`)
	fp16Pattern     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hostPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*\.?$`)
)

// browseEvent is a line of dns-sd -B: an instance added or removed on an
// interface.
type browseEvent struct {
	add      bool
	iface    string
	instance string
}

// parseBrowseLine reads "12:00:01.123  Add  2  4 local.  _musubi-phone._tcp.  musubi-0a1b2c3d".
// Only phones' instance names are kept.
func parseBrowseLine(line string) (browseEvent, bool) {
	f := strings.Fields(line)
	if len(f) != 7 || (f[1] != "Add" && f[1] != "Rmv") || f[4] != "local." || f[5] != ServiceType+"." {
		return browseEvent{}, false
	}
	name := strings.ToLower(f[6])
	if !instancePattern.MatchString(name) {
		return browseEvent{}, false
	}
	return browseEvent{add: f[1] == "Add", iface: f[3], instance: name}, true
}

// sighting is a phone dns-sd -L resolved: where it listens and its TXT.
type sighting struct {
	host string
	port uint16
	txt  map[string]string
}

// parseReachLine reads "… can be reached at host.local.:47616 (interface 4)".
func parseReachLine(line string) (sighting, bool) {
	_, rest, ok := strings.Cut(line, " can be reached at ")
	if !ok {
		return sighting{}, false
	}
	hp, _, _ := strings.Cut(rest, " ")
	i := strings.LastIndexByte(hp, ':')
	if i <= 0 {
		return sighting{}, false
	}
	host := hp[:i]
	port, err := strconv.ParseUint(hp[i+1:], 10, 16)
	if err != nil || port == 0 || len(host) > 253 || !hostPattern.MatchString(host) {
		return sighting{}, false
	}
	return sighting{host: host, port: uint16(port)}, true
}

// parseTXT reads dns-sd's TXT line (" v=2 fp=… pair=1"; a space or
// backslash in a value is escaped with a backslash). Bounded: at most 16
// entries.
func parseTXT(line string) map[string]string {
	out := map[string]string{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 && len(out) < 16 {
			k, v, _ := strings.Cut(cur.String(), "=")
			if len(k) <= 16 && len(v) <= 64 {
				out[strings.ToLower(k)] = v
			}
		}
		cur.Reset()
	}
	esc := false
	for _, r := range strings.TrimSpace(line) {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\':
			esc = true
		case r == ' ':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// parseAddrLine reads a dns-sd -G line: "… Add 40000002 4 host.local. 192.168.1.20 120".
func parseAddrLine(line string) (netip.Addr, bool) {
	f := strings.Fields(line)
	if len(f) != 7 || f[1] != "Add" {
		return netip.Addr{}, false
	}
	return parseDialAddr(f[5])
}

// browse runs dns-sd -B while ctx lasts, starting it again if it ends.
func (s *Syncer) browse(ctx context.Context) {
	wait := 2 * time.Second
	for ctx.Err() == nil {
		proc, err := s.dnssd("-B", ServiceType, "local.")
		if err != nil {
			s.setLooking(false, err.Error())
			if errors.Is(err, errDNSSDUnsupported) {
				return
			}
			s.cfg.Logf("phone: can't look for phones: %v", err)
		} else {
			s.setLooking(true, "")
			ended := s.followBrowse(ctx, proc)
			if !ended {
				return
			}
			s.setLooking(false, "looking for phones stopped; trying again")
			s.cfg.Logf("phone: dns-sd stopped by itself; starting it again")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Minute)
	}
}

// followBrowse follows dns-sd -B's adds and removes, with a resolver for
// each phone seen. It reports whether dns-sd ended by itself.
func (s *Syncer) followBrowse(ctx context.Context, proc dnssdProc) bool {
	type followed struct {
		ifaces map[string]bool
		cancel context.CancelFunc
	}
	phones := map[string]*followed{}
	defer func() {
		for _, f := range phones {
			f.cancel()
		}
		proc.Stop()
	}()
	for {
		select {
		case <-ctx.Done():
			return false
		case line, ok := <-proc.Lines():
			if !ok {
				return true
			}
			ev, ok := parseBrowseLine(line)
			if !ok {
				continue
			}
			f := phones[ev.instance]
			if ev.add {
				if f == nil {
					if len(phones) >= maxFollowed {
						continue
					}
					rctx, cancel := context.WithCancel(ctx)
					f = &followed{ifaces: map[string]bool{}, cancel: cancel}
					phones[ev.instance] = f
					s.wg.Add(1)
					go func(name string) {
						defer s.wg.Done()
						s.follow(rctx, name)
					}(ev.instance)
				}
				f.ifaces[ev.iface] = true
			} else if f != nil {
				delete(f.ifaces, ev.iface)
				if len(f.ifaces) == 0 {
					f.cancel()
					delete(phones, ev.instance)
				}
			}
		}
	}
}

// follow runs dns-sd -L for one phone until ctx ends, considering it at
// each resolution and, while it stays, every resyncVisible. A dns-sd that
// ends by itself (or is killed) is started again: while the browse still
// lists the phone, nothing else would dial it.
func (s *Syncer) follow(ctx context.Context, instance string) {
	tick := time.NewTicker(resyncVisible)
	defer tick.Stop()
	var last *sighting
	wait := 2 * time.Second
	for {
		proc, err := s.dnssd("-L", instance, ServiceType, "local.")
		if err == nil {
			var ended bool
			last, ended = s.followResolve(ctx, proc, tick, last)
			proc.Stop()
			if !ended {
				return
			}
			s.cfg.Logf("phone: resolving a phone stopped by itself; starting it again")
		} else if errors.Is(err, errDNSSDUnsupported) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Minute)
	}
}

// followResolve reads one dns-sd -L until ctx ends (false) or dns-sd ends
// (true), returning the latest sighting.
func (s *Syncer) followResolve(ctx context.Context, proc dnssdProc, tick *time.Ticker, last *sighting) (*sighting, bool) {
	var pending *sighting
	for {
		select {
		case <-ctx.Done():
			return last, false
		case <-tick.C:
			if last != nil {
				s.consider(ctx, *last, true)
			}
		case line, ok := <-proc.Lines():
			if !ok {
				return last, true
			}
			if pending != nil && strings.HasPrefix(line, " ") {
				pending.txt = parseTXT(line)
				last, pending = pending, nil
				s.consider(ctx, *last, false)
				continue
			}
			if r, ok := parseReachLine(line); ok {
				pending = &r
			}
		}
	}
}

// consider dials a sighted phone when it is paired or asks to pair.
// periodic: it has been seen before, so only a sync that is due.
func (s *Syncer) consider(ctx context.Context, r sighting, periodic bool) {
	if r.txt["v"] != "2" {
		return
	}
	fp16 := strings.ToLower(r.txt["fp"])
	if !fp16Pattern.MatchString(fp16) {
		return
	}
	now := s.cfg.Now()
	t := target{fp16: fp16, pair: r.txt["pair"] == "1"}
	pin, paired := s.pins.byPrefix(fp16)
	wantLive := paired && s.cfg.Live != nil && r.txt["live"] == "1"
	s.mu.Lock()
	if paired {
		s.seen[fp16] = now
	}
	// A live connection closed for being idle is opened again only when
	// the phone advertises anew (it came back to the foreground).
	if !periodic || !wantLive {
		delete(s.liveIdled, fp16)
	}
	if s.liveIdled[fp16] {
		wantLive = false
	}
	tried, synced := s.pairTried[fp16], s.synced[fp16]
	s.mu.Unlock()
	if t.pair && periodic && paired {
		// A pairing seen earlier, since done: the phone's TXT lags.
		t.pair = false
	}
	switch {
	case t.pair:
		if s.pairIgnored(fp16) || (!tried.IsZero() && now.Sub(tried) < pairRetryAfter) {
			return
		}
	case paired:
		gap := resyncGap
		if periodic {
			gap = resyncVisible - time.Second
		}
		// Explore is dialed at once; session syncs first when one is due.
		if !wantLive && !synced.IsZero() && now.Sub(synced) < gap {
			return
		}
		t.pin, t.live = pin.ID, wantLive
	default:
		return
	}
	for _, ip := range s.lookup(ctx, r.host) {
		t.addrs = append(t.addrs, netip.AddrPortFrom(ip, r.port))
	}
	if len(t.addrs) == 0 {
		if s.unreachableLog.ok(now) {
			s.cfg.Logf("phone: a phone was seen but has no private address to dial")
		}
		return
	}
	s.start(ctx, t)
}

// lookup resolves host to the addresses the Mac may dial, IPv4 first.
func (s *Syncer) lookup(ctx context.Context, host string) []netip.Addr {
	proc, err := s.dnssd("-G", "v4v6", host)
	if err != nil {
		return nil
	}
	defer proc.Stop()
	var v4, v6 []netip.Addr
	seen := map[netip.Addr]bool{}
	deadline := time.NewTimer(resolveWait)
	defer deadline.Stop()
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return append(v4, v6...)
		case <-settle:
			return append(v4, v6...)
		case line, ok := <-proc.Lines():
			if !ok {
				return append(v4, v6...)
			}
			ip, ok := parseAddrLine(line)
			if !ok || seen[ip] || len(v4)+len(v6) >= maxAddrs {
				continue
			}
			seen[ip] = true
			if ip.Is4() {
				v4 = append(v4, ip)
			} else {
				v6 = append(v6, ip)
			}
			if settle == nil {
				settle = time.After(resolveSettle)
			}
		}
	}
}
