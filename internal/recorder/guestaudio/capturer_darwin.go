//go:build darwin

package guestaudio

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/anielsud/musubi/internal/recorder/audio"
)

// WindowCapturer adapts a Tap's push-based callback to the pull-based
// Start/Stop/Samples/Reset/Close shape internal/audio.Capturer expects
// (structural typing — this type needs no import of that interface
// itself), so it plugs into audio.NewStreamCapturer exactly like the
// mic's malgo-based Capturer does. Also resamples from ScreenCaptureKit's
// 48kHz down to audio.CaptureSampleRate, since nothing downstream (VAD,
// Parakeet TDT) resamples on its own.
//
// Despite the name, also backs the system-wide ("Everything") capture
// path (see NewSystemCapturer) — the wrapping logic here is identical
// either way; only which Tap it wraps differs.
type WindowCapturer struct {
	tap *Tap

	mu      sync.Mutex
	samples []float32
	started bool
	// rs brings the tap's buffers down to the pipeline's rate. It holds
	// filter state from one buffer to the next, so one is kept per
	// stream and only replaced when the tap reports another rate (rsRate).
	rs     *audio.Resampler
	rsRate int
	// heard is when audio last arrived (unix nanoseconds; 0 before any).
	// ScreenCaptureKit sends buffers through silence too, so a stream
	// that goes quiet has stopped (its window closed).
	heard atomic.Int64
}

// LastHeard is when the capture last delivered audio (zero before any).
func (wc *WindowCapturer) LastHeard() time.Time {
	if n := wc.heard.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// NewWindowCapturer creates a Capturer-shaped wrapper around a Tap for
// windowID (e.g. from teamsvideo.FindMeetingWindow or
// teamsvideo.FindWindowForPID).
func NewWindowCapturer(windowID uint32) *WindowCapturer {
	wc := &WindowCapturer{}
	wc.tap = NewTap(windowID, wc.onSamples)
	return wc
}

// NewSystemCapturer creates a Capturer-shaped wrapper around a
// system-wide Tap (the source picker's "Everything" option).
func NewSystemCapturer() *WindowCapturer {
	wc := &WindowCapturer{}
	wc.tap = NewSystemTap(wc.onSamples)
	return wc
}

func (wc *WindowCapturer) onSamples(samples []float32, sampleRateHz float64) {
	wc.heard.Store(time.Now().UnixNano())
	in := int(sampleRateHz)

	wc.mu.Lock()
	if wc.rs == nil || wc.rsRate != in {
		wc.rs, wc.rsRate = audio.NewResampler(in, audio.CaptureSampleRate), in
	}
	out := wc.rs.Process(samples)
	wc.samples = append(wc.samples, out...)
	wc.mu.Unlock()
}

// Start is idempotent: meetingaudio_darwin.go calls it once up front to
// confirm the tap actually works (Screen Recording permission, etc.)
// before handing this capturer to a StreamCapturer, which will call
// Start() again itself. A second Start on the same Tap would find its
// callback handle already unregistered by Stop (see Tap.Stop), so this
// deliberately never stops and restarts the underlying tap -- it just
// starts it once, real capture keeping running across both calls.
func (wc *WindowCapturer) Start() error {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if wc.started {
		return nil
	}
	if err := wc.tap.Start(); err != nil {
		return err
	}
	wc.started = true
	return nil
}

func (wc *WindowCapturer) Stop() error {
	wc.tap.Stop()
	return nil
}

func (wc *WindowCapturer) Samples() []float32 {
	wc.mu.Lock()
	defer wc.mu.Unlock()

	out := make([]float32, len(wc.samples))
	copy(out, wc.samples)
	return out
}

func (wc *WindowCapturer) Reset() {
	wc.mu.Lock()
	wc.samples = wc.samples[:0]
	wc.mu.Unlock()
}

func (wc *WindowCapturer) Close() {
	wc.tap.Stop()
}
