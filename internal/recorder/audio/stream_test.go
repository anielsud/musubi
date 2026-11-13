package audio

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// mockCapturer is a Capturer whose buffer the test fills with feed.
type mockCapturer struct {
	mu                       sync.Mutex
	buf                      []float32
	startErr                 error
	started, stopped, closed bool
}

func (m *mockCapturer) feed(s []float32) {
	m.mu.Lock()
	m.buf = append(m.buf, s...)
	m.mu.Unlock()
}

func (m *mockCapturer) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.started = m.startErr == nil
	return m.startErr
}

func (m *mockCapturer) Stop() error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	return nil
}

func (m *mockCapturer) Samples() []float32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]float32(nil), m.buf...)
}

func (m *mockCapturer) Reset() {
	m.mu.Lock()
	m.buf = nil
	m.mu.Unlock()
}

func (m *mockCapturer) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

// fakeTicker ticks when the test sends on ch.
type fakeTicker struct{ ch chan time.Time }

func (f *fakeTicker) Ticks() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()                   {}

// handTicked starts a StreamCapturer over a fresh mock whose poll loop
// only runs when the returned tick function is called.
func handTicked(t *testing.T, window, chanBuf int) (*StreamCapturer, *mockCapturer, func()) {
	t.Helper()
	ft := &fakeTicker{ch: make(chan time.Time)}
	prev := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	t.Cleanup(func() { makeTickerFunc = prev })

	m := &mockCapturer{}
	sc := NewStreamCapturer(m, window, chanBuf)
	if err := sc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// An unbuffered send returns once the loop has taken the tick; a
	// second one returns only after the first tick's work is done.
	tick := func() {
		ft.ch <- time.Now()
		ft.ch <- time.Now()
	}
	return sc, m, tick
}

func TestStreamCapturerCutsWindowsInOrder(t *testing.T) {
	sc, m, tick := handTicked(t, 3, 10)
	m.feed([]float32{1, 2, 3, 4})
	tick()
	m.feed([]float32{5, 6, 7})
	tick()
	if err := sc.Stop(); err != nil {
		t.Fatal(err)
	}

	var got [][]float32
	for w := range sc.Windows() {
		got = append(got, w)
	}
	want := [][]float32{{1, 2, 3}, {4, 5, 6}}
	if len(got) != len(want) {
		t.Fatalf("got %d windows %v, want %v (the trailing partial window stays unsent)", len(got), got, want)
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("window %d = %v, want %v", i, got[i], want[i])
			}
		}
	}
	if all := sc.AllSamples(); len(all) != 7 || all[6] != 7 {
		t.Errorf("AllSamples = %v, want all 7 samples including the partial window", all)
	}
	if n, r := sc.Recorded(), sc.RecordedRange(-2, 3); n != 7 || len(r) != 3 || r[2] != 3 {
		t.Errorf("Recorded = %d, RecordedRange(-2, 3) = %v", n, r)
	}
	if r := sc.RecordedRange(5, 99); len(r) != 2 || r[1] != 7 {
		t.Errorf("RecordedRange(5, 99) = %v", r)
	}
	if r := sc.RecordedRange(9, 12); r != nil {
		t.Errorf("RecordedRange past the end = %v", r)
	}
}

func TestStreamCapturerStopFlushesUnpolledAudio(t *testing.T) {
	sc, m, _ := handTicked(t, 2, 10)
	m.feed([]float32{1, 2, 3, 4})
	if err := sc.Stop(); err != nil {
		t.Fatal(err)
	}
	n := 0
	for w := range sc.Windows() {
		n += len(w)
	}
	if n != 4 {
		t.Errorf("windows after stop carried %d samples, want 4", n)
	}
	if !m.stopped {
		t.Error("device not stopped")
	}
	if err := sc.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStreamCapturerStartErrors(t *testing.T) {
	m := &mockCapturer{startErr: errors.New("no mic")}
	sc := NewStreamCapturer(m, 4, 1)
	err := sc.Start()
	if !errors.Is(err, m.startErr) {
		t.Fatalf("Start = %v, want it to wrap the device error", err)
	}
	if err := sc.Stop(); err != nil {
		t.Errorf("Stop after a failed Start = %v, want nil", err)
	}

	sc.Close()
	if !m.closed {
		t.Error("Close didn't close the device")
	}
	if err := sc.Start(); err == nil {
		t.Error("Start after Close succeeded")
	}
	sc.Close() // must not panic or close the device twice over
}

func TestStreamCapturerRejectsSecondStart(t *testing.T) {
	sc, _, _ := handTicked(t, 4, 1)
	if err := sc.Start(); err == nil {
		t.Error("second Start succeeded")
	}
	sc.Close()
	if _, open := <-sc.Windows(); open {
		t.Error("Close of a running capturer left Windows open")
	}
}

func TestNewStreamCapturerDefaults(t *testing.T) {
	sc := NewStreamCapturer(&mockCapturer{}, -3, -1)
	if sc.windowSize != DefaultWindowSize || cap(sc.windowCh) != 0 {
		t.Errorf("window %d, buffer %d; want %d and 0", sc.windowSize, cap(sc.windowCh), DefaultWindowSize)
	}
}

// A consumer that falls far behind still gets every window, in order:
// audio beyond the channel's buffer is held back, not dropped.
func TestStreamCapturerHoldsBackForSlowConsumer(t *testing.T) {
	mock := &mockCapturer{}
	sc := NewStreamCapturer(mock, 4, 2)
	ft := &fakeTicker{ch: make(chan time.Time, 100)}
	origMaker := makeTickerFunc
	makeTickerFunc = func() tickerIface { return ft }
	defer func() { makeTickerFunc = origMaker }()
	if err := sc.Start(); err != nil {
		t.Fatal(err)
	}
	// 50 ticks of 8 samples with nobody reading: 100 windows against a
	// channel of 2.
	for i := 0; i < 50; i++ {
		var batch []float32
		for j := 0; j < 8; j++ {
			batch = append(batch, float32(i*8+j))
		}
		mock.feed(batch)
		ft.ch <- time.Now()
	}
	time.Sleep(50 * time.Millisecond)
	if sc.Behind() == 0 {
		t.Error("nothing reported held back")
	}
	done := make(chan []float32)
	go func() {
		var all []float32
		for w := range sc.Windows() {
			all = append(all, w...)
			time.Sleep(time.Millisecond) // a slow consumer
		}
		done <- all
	}()
	for sc.Behind() > 0 {
		ft.ch <- time.Now()
		time.Sleep(5 * time.Millisecond)
	}
	if err := sc.Stop(); err != nil {
		t.Fatal(err)
	}
	all := <-done
	if len(all) != 400 {
		t.Fatalf("got %d samples, want 400", len(all))
	}
	for i, v := range all {
		if v != float32(i) {
			t.Fatalf("sample %d is %v: out of order", i, v)
		}
	}
	if sc.Dropped() != 0 {
		t.Errorf("dropped %d samples", sc.Dropped())
	}
}
