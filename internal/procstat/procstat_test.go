package procstat

import (
	"encoding/binary"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// fake is a process table in memory.
type fake struct {
	mu    sync.Mutex
	procs []Proc
	args  map[int][]string
	usage map[int]Usage
	resp  map[int]int
	// noResp: a system without the responsible-pid API.
	noResp bool
	reads  int
}

func (f *fake) Procs() ([]Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return slices.Clone(f.procs), nil
}

func (f *fake) Args(pid int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.args[pid]; ok {
		return a, nil
	}
	return nil, errors.New("no args")
}

func (f *fake) Usage(pid int) (Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.usage[pid]; ok {
		return u, nil
	}
	return Usage{}, errors.New("gone")
}

func (f *fake) Responsible(pid int) (int, bool) {
	if f.noResp {
		return 0, false
	}
	if r, ok := f.resp[pid]; ok {
		return r, true
	}
	return pid, true
}

var t0 = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

const (
	self     = 500
	webkit   = "/System/Library/Frameworks/WebKit.framework/Versions/A/XPCServices/com.apple.WebKit.WebContent.xpc/Contents/MacOS/com.apple.WebKit.WebContent"
	recorder = "/Applications/Musubi Recorder.app/Contents/MacOS/musubi-recorder"
)

func table() *fake {
	return &fake{
		procs: []Proc{
			{PID: 1, PPID: 0, Path: "/sbin/launchd"},
			{PID: self, PPID: 1, Path: "/Applications/Musubi.app/Contents/MacOS/musubi-gui"},
			{PID: 501, PPID: 1, Path: webkit},     // Musubi's web view
			{PID: 502, PPID: 1, Path: webkit},     // another app's
			{PID: 600, PPID: 1, Path: recorder},   // the recorder
			{PID: 601, PPID: 600, Path: recorder}, // its diarize worker
			{PID: 602, PPID: 601, Path: "/opt/homebrew/bin/ffmpeg"},
			{PID: 700, PPID: 1, Path: "/opt/homebrew/bin/ollama"}, // ollama serve
			{PID: 701, PPID: 700, Path: "/opt/homebrew/lib/ollama/llama-server"},
			{PID: 702, PPID: 700, Path: "/opt/homebrew/lib/ollama/llama-server"},
			{PID: 710, PPID: 90, Path: "/opt/homebrew/bin/ollama"}, // `ollama run` in a shell
			{PID: 800, PPID: 1, Path: "/Applications/Tomoe.app/Contents/MacOS/tomoe-gui"},
			{PID: 900, PPID: 1, Path: "/usr/bin/other"},
		},
		args: map[int][]string{
			600: {"musubi-recorder", "run"},
			601: {"musubi-recorder", "diarize"},
			700: {"ollama", "serve"},
			710: {"ollama", "run", "qwen3:8b"},
		},
		resp:  map[int]int{501: self, 502: 77},
		usage: map[int]Usage{},
	}
}

func keys(gs []Group) []string {
	var out []string
	for _, g := range gs {
		out = append(out, g.Key)
	}
	return out
}

func pids(g Group) []int {
	var out []int
	for _, m := range g.Members {
		out = append(out, m.PID)
	}
	return out
}

func byKey(gs []Group, k string) Group {
	for _, g := range gs {
		if g.Key == k {
			return g
		}
	}
	return Group{}
}

func TestFindGroupsComponentsWithChildren(t *testing.T) {
	f := table()
	gs := Find(f.procs, f, self, 0)
	if got, want := keys(gs), []string{KeyMusubi, KeyWebView, KeyRecorder, KeyOllama, KeyTomoe}; !slices.Equal(got, want) {
		t.Fatalf("components %v, want %v", got, want)
	}
	for k, want := range map[string][]int{
		KeyMusubi:   {self},
		KeyWebView:  {501},
		KeyRecorder: {600, 601, 602},
		KeyOllama:   {700, 701, 702},
		KeyTomoe:    {800},
	} {
		if got := pids(byKey(gs, k)); !slices.Equal(got, want) {
			t.Errorf("%s: pids %v, want %v", k, got, want)
		}
	}
	rec := byKey(gs, KeyRecorder)
	if rec.Members[1].Name != "musubi-recorder diarize" || rec.Members[2].Name != "ffmpeg" {
		t.Errorf("recorder parts named %q, %q", rec.Members[1].Name, rec.Members[2].Name)
	}
}

func TestFindPrefersReportedRecorderPID(t *testing.T) {
	f := table()
	// A dev build run from a folder: no app path, no `run` argument
	// readable; the pid on the control socket is what finds it.
	f.procs = append(f.procs, Proc{PID: 650, PPID: 1, Path: "/src/bin/musubi-recorder"})
	gs := Find(f.procs, f, self, 650)
	if got := pids(byKey(gs, KeyRecorder)); !slices.Equal(got, []int{650}) {
		t.Fatalf("recorder pids %v, want [650]", got)
	}
	// A reported pid now held by something else is not believed.
	gs = Find(f.procs, f, self, 900)
	if got := pids(byKey(gs, KeyRecorder)); !slices.Equal(got, []int{600, 601, 602}) {
		t.Fatalf("recorder pids %v after a reused pid", got)
	}
}

func TestFindLeavesOutWebViewWithoutResponsibility(t *testing.T) {
	f := table()
	f.noResp = true
	gs := Find(f.procs, f, self, 0)
	if g := byKey(gs, KeyWebView); len(g.Members) != 0 {
		t.Fatalf("web view guessed without the API: %v", pids(g))
	}
}

func TestFindCountsAProcessOnce(t *testing.T) {
	f := table()
	// The recorder launched by Musubi: it stays the recorder's, not
	// Musubi's descendant too.
	f.procs[4].PPID = self
	gs := Find(f.procs, f, self, 0)
	if got := pids(byKey(gs, KeyMusubi)); !slices.Equal(got, []int{self}) {
		t.Fatalf("Musubi pids %v", got)
	}
	if got := pids(byKey(gs, KeyRecorder)); !slices.Equal(got, []int{600, 601, 602}) {
		t.Fatalf("recorder pids %v", got)
	}
}

func TestCPUPercent(t *testing.T) {
	start := t0.Add(-time.Hour)
	prevAt := t0
	wall := 2 * time.Second
	cases := []struct {
		name  string
		prev  prevUsage
		start time.Time
		cpu   time.Duration
		want  float64
	}{
		{"one core busy", prevUsage{true, start, 10 * time.Second}, start, 12 * time.Second, 100},
		{"quarter core", prevUsage{true, start, 10 * time.Second}, start, 10500 * time.Millisecond, 25},
		{"three cores", prevUsage{true, start, 10 * time.Second}, start, 16 * time.Second, 300},
		{"started in the interval", prevUsage{}, t0.Add(time.Second), 500 * time.Millisecond, 25},
		{"reused pid", prevUsage{true, start, 50 * time.Second}, t0.Add(time.Second), time.Second, 50},
		{"not read last time", prevUsage{}, start, 99 * time.Second, 0},
		{"clock went back", prevUsage{true, start, 10 * time.Second}, start, 9 * time.Second, 0},
	}
	for _, c := range cases {
		if got := cpuPercent(c.prev, c.start, prevAt, c.cpu, wall); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRing(t *testing.T) {
	r := NewRing[int](3)
	if r.Len() != 0 || len(r.Slice()) != 0 {
		t.Fatal("new ring not empty")
	}
	r.Push(1)
	r.Push(2)
	if got := r.Slice(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("%v", got)
	}
	for _, v := range []int{3, 4, 5} {
		r.Push(v)
	}
	if got := r.Slice(); !slices.Equal(got, []int{3, 4, 5}) || r.Len() != 3 {
		t.Fatalf("%v", got)
	}
	s := r.Slice()
	s[0] = 99
	if r.Slice()[0] != 3 {
		t.Fatal("Slice shares the buffer")
	}
}

// clock is a settable time for the sampler.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestSamplerSumsComponents(t *testing.T) {
	f := table()
	for _, p := range f.procs {
		f.usage[p.PID] = Usage{CPU: 10 * time.Second, RSS: 100, Footprint: 50}
	}
	c := &clock{t: t0}
	s := NewSampler(f, Machine{Cores: 8}, self, nil, nil)
	s.now = c.now
	s.mu.Lock()
	if _, ok := s.sampleLocked(); ok {
		t.Fatal("baseline counted as a sample")
	}
	c.add(2 * time.Second)
	f.usage[700] = Usage{CPU: 11 * time.Second, RSS: 100, Footprint: 50} // serve: half a core
	f.usage[701] = Usage{CPU: 14 * time.Second, RSS: 100, Footprint: 50} // runner: two cores
	smp, ok := s.sampleLocked()
	s.mu.Unlock()
	if !ok {
		t.Fatal("no sample")
	}
	var ol Reading
	for _, r := range smp.Components {
		if r.Key == KeyOllama {
			ol = r
		}
	}
	if ol.CPU != 250 || ol.RSS != 300 || ol.Footprint != 150 || len(ol.Parts) != 3 {
		t.Fatalf("ollama %+v", ol)
	}
	if ol.Parts[1].Name != "llama-server" || ol.Parts[1].CPU != 200 {
		t.Fatalf("runner part %+v", ol.Parts[1])
	}
}

func TestSamplerWatchStartsAndStops(t *testing.T) {
	f := table()
	for _, p := range f.procs {
		f.usage[p.PID] = Usage{CPU: time.Second, RSS: 1}
	}
	got := make(chan Sample, 100)
	s := NewSampler(f, Machine{}, self, func() int { return 0 }, func(x Sample) { got <- x })
	s.interval = 5 * time.Millisecond
	if s.Running() {
		t.Fatal("running before a watch")
	}
	snap := s.Watch()
	if !snap.Supported || len(snap.History) != 0 {
		t.Fatalf("first snapshot %+v", snap)
	}
	for range 3 {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("no samples while watched")
		}
	}
	s.Unwatch()
	if s.Running() {
		t.Fatal("still running after unwatch")
	}
	f.mu.Lock()
	reads := f.reads
	f.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
	f.mu.Lock()
	after := f.reads
	f.mu.Unlock()
	if after != reads {
		t.Fatalf("table read %d times after unwatch", after-reads)
	}
	if n := len(s.Watch().History); n < 3 {
		t.Fatalf("history lost: %d samples", n)
	}
	s.Unwatch()
	s.Unwatch() // twice is fine
}

func TestSamplerStopsWhenLeaseLapses(t *testing.T) {
	f := table()
	c := &clock{t: t0}
	s := NewSampler(f, Machine{}, self, nil, nil)
	s.interval = 5 * time.Millisecond
	s.lease = time.Minute
	s.now = c.now
	s.Watch()
	c.add(2 * time.Minute)
	deadline := time.Now().Add(2 * time.Second)
	for s.Running() {
		if time.Now().After(deadline) {
			t.Fatal("sampler outlived its lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestParseProcArgs(t *testing.T) {
	b := binary.LittleEndian.AppendUint32(nil, 2)
	b = append(b, "/opt/homebrew/bin/ollama\x00\x00\x00\x00ollama\x00serve\x00OLLAMA_HOST=x\x00"...)
	a, err := parseProcArgs(b)
	if err != nil || !slices.Equal(a, []string{"ollama", "serve"}) {
		t.Fatalf("%v %v", a, err)
	}
	for _, bad := range [][]byte{nil, {1, 0}} {
		if _, err := parseProcArgs(bad); err == nil {
			t.Errorf("short buffer %v accepted", bad)
		}
	}
	huge := binary.LittleEndian.AppendUint32(nil, 1<<30)
	if _, err := parseProcArgs(append(huge, "x\x00y\x00"...)); err == nil {
		t.Error("absurd argc accepted")
	}
}
