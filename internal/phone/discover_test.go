package phone

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeDNSSD stands in for /usr/bin/dns-sd: each run prints script's lines
// for its arguments, then stays up, as dns-sd does, until stopped.
type fakeDNSSD struct {
	mu     sync.Mutex
	script func(args []string) []string
	runs   []*fakeRun
}

type fakeRun struct {
	args    []string
	lines   chan string
	quit    chan struct{}
	ended   chan struct{}
	once    sync.Once
	stopped bool
}

func (f *fakeDNSSD) start(args ...string) (dnssdProc, error) {
	r := &fakeRun{args: args, lines: make(chan string), quit: make(chan struct{}), ended: make(chan struct{})}
	f.mu.Lock()
	f.runs = append(f.runs, r)
	f.mu.Unlock()
	out := f.script(args)
	go func() {
		defer close(r.ended)
		defer close(r.lines)
		for _, l := range out {
			select {
			case r.lines <- l:
			case <-r.quit:
				return
			}
		}
		<-r.quit
	}()
	return r, nil
}

func (r *fakeRun) Lines() <-chan string { return r.lines }
func (r *fakeRun) Stop() {
	r.once.Do(func() { r.stopped = true; close(r.quit) })
	<-r.ended
}

func (f *fakeDNSSD) ran(prefix ...string) []*fakeRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*fakeRun
	for _, r := range f.runs {
		if len(r.args) >= len(prefix) && reflect.DeepEqual(r.args[:len(prefix)], prefix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeDNSSD) allStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		select {
		case <-r.ended:
		default:
			return false
		}
	}
	return true
}

func browseLine(op, instance string) string {
	return fmt.Sprintf("19:56:40.074  %s        3   1 local.               _musubi-phone._tcp.  %s", op, instance)
}

func lookupLines(instance, host string, port uint16, txt string) []string {
	return []string{
		"Lookup " + instance + "._musubi-phone._tcp.local.",
		"DATA: ---Fri 13 Nov 2026---",
		"19:56:40.065  ...STARTING...",
		fmt.Sprintf("19:56:40.065  %s._musubi-phone._tcp.local. can be reached at %s:%d (interface 16) Flags: 1", instance, host, port),
		" " + txt,
	}
}

func addrLines(host string, addrs ...string) []string {
	out := []string{"DATE: ---Fri 13 Nov 2026---", "19:56:42.095  ...STARTING...",
		"Timestamp     A/R  Flags         IF  Hostname                               Address                                      TTL"}
	for _, a := range addrs {
		out = append(out, fmt.Sprintf("19:56:42.096  Add  40000002      16  %s                        %s                                4500", host, a))
	}
	return out
}

func TestParseDNSSDLines(t *testing.T) {
	ev, ok := parseBrowseLine(browseLine("Add", "musubi-0A1B2C3D"))
	if !ok || !ev.add || ev.instance != "musubi-0a1b2c3d" || ev.iface != "1" {
		t.Errorf("browse %+v %v", ev, ok)
	}
	for _, bad := range []string{
		"Timestamp     A/R    Flags  if Domain               Service Type         Instance Name",
		browseLine("Add", "Musubi on Studio"), browseLine("Add", "musubi-0a1b2c3"), browseLine("Add", "printer"),
		"19:56:40.074  Add        3   1 local.               _other._tcp.  musubi-0a1b2c3d",
	} {
		if _, ok := parseBrowseLine(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
	if ev, ok := parseBrowseLine(browseLine("Rmv", "musubi-0a1b2c3d")); !ok || ev.add {
		t.Errorf("rmv %+v", ev)
	}
	r, ok := parseReachLine(lookupLines("musubi-0a1b2c3d", "Pixel-7.local.", 47616, "")[3])
	if !ok || r.host != "Pixel-7.local." || r.port != 47616 {
		t.Errorf("reach %+v %v", r, ok)
	}
	for _, bad := range []string{"… can be reached at host.local.:0 (interface 1)", "… can be reached at host.local.:99999 (interface 1)",
		"… can be reached at $(rm -rf).local.:47616 (interface 1)", "… can be reached at nohost (interface 1)"} {
		if _, ok := parseReachLine(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
	txt := parseTXT(` v=2 fp=0a1b2c3d4e5f6071 pair=1 note=a\ b\\c`)
	if !reflect.DeepEqual(txt, map[string]string{"v": "2", "fp": "0a1b2c3d4e5f6071", "pair": "1", "note": `a b\c`}) {
		t.Errorf("txt %v", txt)
	}
	if n := len(parseTXT(strings.Repeat(" k=v x=y", 40))); n > 16 {
		t.Errorf("%d TXT entries kept", n)
	}
	var got []string
	for _, l := range addrLines("phone.local.", "192.168.68.61", "FE80:0000:0000:0000:0071:83EA:D0E4:EB68%en0", "8.8.8.8", "fe80::2") {
		if ip, ok := parseAddrLine(l); ok {
			got = append(got, ip.String())
		}
	}
	if !reflect.DeepEqual(got, []string{"192.168.68.61", "fe80::71:83ea:d0e4:eb68%en0"}) {
		t.Errorf("addresses %v", got)
	}
}

func TestDiscovery(t *testing.T) {
	h := newHarness(t)
	paired, stranger, asking := newFakePhone(t), newFakePhone(t), newFakePhone(t)
	h.pairWith(paired)
	asking.pairing = true
	inst := func(p *fakePhone) string { return "musubi-" + p.fpHex()[:8] }
	f := &fakeDNSSD{script: func(args []string) []string {
		switch args[0] {
		case "-B":
			return []string{"Browsing for _musubi-phone._tcp.local.", browseLine("Add", inst(paired)), browseLine("Add", inst(paired)),
				browseLine("Add", inst(stranger)), browseLine("Add", "printer"), browseLine("Add", inst(asking))}
		case "-L":
			switch args[1] {
			case inst(paired):
				return lookupLines(args[1], "paired.local.", paired.port, "v=2 fp="+paired.fpHex()[:16])
			case inst(stranger):
				return lookupLines(args[1], "stranger.local.", stranger.port, "v=2 fp="+stranger.fpHex()[:16])
			case inst(asking):
				return lookupLines(args[1], "asking.local.", asking.port, "v=2 fp="+asking.fpHex()[:16]+" pair=1")
			}
		case "-G":
			return addrLines(args[2], "8.8.8.8", "127.0.0.1")
		}
		return nil
	}}
	h.s.dnssd = f.start
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.s.Run(ctx); close(stopped) }()

	waitFor(t, "the paired phone's sync", func() bool { return strings.HasSuffix(strings.Join(paired.opList(), " "), "bye") })
	if ops := strings.Join(paired.opList(), " "); ops != "hello list agenda bye" {
		t.Errorf("paired: %s", ops)
	}
	if on, why := h.s.Looking(); !on || why != "" {
		t.Errorf("looking %v %q", on, why)
	}
	waitFor(t, "the pairing prompt", func() bool { return h.s.PairPrompt() != nil })
	pr := h.s.PairPrompt()
	h.s.AnswerPair(pr.ID, true)
	waitFor(t, "the new phone's sync", func() bool { return strings.HasSuffix(strings.Join(asking.opList(), " "), "bye") })
	if len(h.s.Devices()) != 2 {
		t.Errorf("devices %v", h.s.Devices())
	}
	if len(f.ran("-L")) != 3 {
		t.Errorf("%d phones followed (the paired one twice on two interfaces?)", len(f.ran("-L")))
	}
	for _, r := range f.ran("-G") {
		if r.args[2] == "stranger.local." {
			t.Error("an unpaired phone not asking to pair was looked up")
		}
	}
	if len(stranger.opList()) != 0 {
		t.Error("an unpaired phone was dialed")
	}

	cancel()
	<-stopped
	if !f.allStopped() {
		t.Error("a dns-sd outlived phone sync")
	}
	for _, r := range f.runs {
		if !r.stopped {
			t.Errorf("dns-sd %v ended without Stop", r.args)
		}
	}
	if on, _ := h.s.Looking(); on {
		t.Error("still looking")
	}
}

func TestDiscoveryRemoved(t *testing.T) {
	h := newHarness(t)
	f := &fakeDNSSD{}
	f.script = func(args []string) []string {
		if args[0] == "-B" {
			// Seen on two interfaces, gone from one: still followed.
			return []string{
				"19:56:40.074  Add        3   1 local.               _musubi-phone._tcp.  musubi-0a1b2c3d",
				"19:56:40.074  Add        3  16 local.               _musubi-phone._tcp.  musubi-0a1b2c3d",
				"19:56:40.074  Rmv        1   1 local.               _musubi-phone._tcp.  musubi-0a1b2c3d",
				"19:56:40.074  Add        3   1 local.               _musubi-phone._tcp.  musubi-11111111",
				"19:56:40.074  Rmv        1   1 local.               _musubi-phone._tcp.  musubi-11111111",
			}
		}
		return nil
	}
	h.s.dnssd = f.start
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.s.Run(ctx); close(stopped) }()
	waitFor(t, "both followed", func() bool { return len(f.ran("-L")) == 2 })
	waitFor(t, "the removed one let go", func() bool {
		for _, r := range f.ran("-L", "musubi-11111111") {
			select {
			case <-r.ended:
				return true
			default:
			}
		}
		return false
	})
	for _, r := range f.ran("-L", "musubi-0a1b2c3d") {
		select {
		case <-r.ended:
			t.Error("a phone still on one interface was let go")
		default:
		}
	}
	cancel()
	<-stopped
	if !f.allStopped() {
		t.Error("a dns-sd outlived phone sync")
	}
}

func TestDiscoveryUnsupported(t *testing.T) {
	h := newHarness(t)
	h.s.dnssd = func(...string) (dnssdProc, error) { return nil, errDNSSDUnsupported }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.s.Run(ctx); close(stopped) }()
	waitFor(t, "the reason", func() bool { _, why := h.s.Looking(); return why != "" })
	cancel()
	<-stopped
}

// A phone's dns-sd -L killed from outside (2026-11-13: a stray pkill) is
// started again; otherwise the phone stays "seen" and is never dialed.
func TestFollowRestartsResolver(t *testing.T) {
	h := newHarness(t)
	p := newFakePhone(t)
	h.pairWith(p)
	inst := "musubi-" + p.fpHex()[:8]
	f := &fakeDNSSD{script: func(args []string) []string {
		switch args[0] {
		case "-B":
			return []string{"Browsing for _musubi-phone._tcp.local.", browseLine("Add", inst)}
		case "-L":
			return lookupLines(args[1], "paired.local.", p.port, "v=2 fp="+p.fpHex()[:16])
		case "-G":
			return addrLines(args[2], "127.0.0.1")
		}
		return nil
	}}
	h.s.dnssd = f.start
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.s.Run(ctx); close(stopped) }()
	waitFor(t, "the phone followed", func() bool { return len(f.ran("-L")) == 1 })
	f.ran("-L")[0].Stop()
	waitFor(t, "the resolver started again", func() bool { return len(f.ran("-L")) == 2 })
	cancel()
	<-stopped
	if !f.allStopped() {
		t.Error("a dns-sd was left running")
	}
}
