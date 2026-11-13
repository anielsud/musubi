package phone

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// serve runs the server on a loopback port until the returned stop is
// called; stop waits for it to finish.
func serve(t *testing.T, h *harness) (port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.s.serveOn(ctx, ln) }()
	return ln.Addr().(*net.TCPAddr).Port, func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAdvertiseWhileServing(t *testing.T) {
	h := newHarness(t)
	port, stop := serve(t, h)
	waitFor(t, "the advertisement", func() bool { on, _, _ := h.s.Advertising(); return on })
	ad := h.ads.last()
	if !strings.HasPrefix(ad.instance, "Musubi on ") || ad.port != port {
		t.Errorf("advertised %q on %d, listening on %d", ad.instance, ad.port, port)
	}
	if want := []string{"v=1", "fp=" + h.s.Fingerprint()[:16]}; !reflect.DeepEqual(ad.txt, want) {
		t.Errorf("TXT %v, want %v", ad.txt, want)
	}
	if fp := strings.TrimPrefix(ad.txt[1], "fp="); fp != strings.ToLower(fp) || len(fp) != 16 {
		t.Errorf("fp %q", fp)
	}
	stop()
	if !ad.proc.stopped {
		t.Error("advertisement left running after the server stopped")
	}
	if on, _, _ := h.s.Advertising(); on {
		t.Error("still reported as advertised")
	}
	// Serving again advertises again.
	_, stop = serve(t, h)
	waitFor(t, "the second advertisement", func() bool { on, _, _ := h.s.Advertising(); return on })
	stop()
	if n := len(h.ads.calls); n != 2 || !h.ads.calls[1].proc.stopped {
		t.Errorf("%d advertisements", n)
	}
}

func TestAdvertiseEndsByItself(t *testing.T) {
	h := newHarness(t)
	_, stop := serve(t, h)
	defer stop()
	waitFor(t, "the advertisement", func() bool { on, _, _ := h.s.Advertising(); return on })
	h.ads.last().proc.end() // dns-sd died
	waitFor(t, "the end", func() bool { on, _, why := h.s.Advertising(); return !on && why != "" })
}

func TestAdvertiseUnavailable(t *testing.T) {
	h := newHarness(t)
	h.ads.err = errAdvertiseUnsupported
	_, stop := serve(t, h)
	waitFor(t, "the attempt", func() bool { _, _, why := h.s.Advertising(); return why != "" })
	stop()
	h = newHarness(t)
	h.ads.err = errors.New("dns-sd missing")
	_, stop = serve(t, h)
	waitFor(t, "the attempt", func() bool { _, _, why := h.s.Advertising(); return why != "" })
	stop()
	if !strings.Contains(h.logged(), "not advertised") {
		t.Error("failure not logged")
	}
}

func TestInstanceName(t *testing.T) {
	for in, want := range map[string]string{
		"Studio":                "Musubi on Studio",
		"  \x00\n ":             "Musubi on this Mac",
		"A‮B":                   "Musubi on AB",
		strings.Repeat("é", 40): "Musubi on " + strings.Repeat("é", 26),
	} {
		if got := instanceName(in); got != want {
			t.Errorf("instanceName(%q) = %q, want %q", in, got, want)
		}
		if len(instanceName(in)) > 63 {
			t.Errorf("%q too long", in)
		}
	}
}
