package audio

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultWindowSize is Silero VAD's input length in samples, the unit the
// live pipeline consumes.
const DefaultWindowSize = 512

// WindowBuffer is the window channel's length (about 32 s at
// DefaultWindowSize). Audio beyond it is held back in the capturer, in
// order, for as long as the consumer is behind: an overloaded machine
// costs time, never audio. (With 128 windows and drops, a slow decode
// once lost minutes of a meeting, and left the diarizer's clock minutes
// behind the transcript's.)
const WindowBuffer = 1000

// stopDrainTimeout bounds how long Stop's drain waits for the consumer.
var stopDrainTimeout = 2 * time.Minute

// StreamCapturer turns a Capturer's growing buffer into a channel of
// equal-sized windows, and also keeps the whole recording so the session
// can save exactly what the live pipeline saw.
type StreamCapturer struct {
	capturer Capturer
	windowCh chan []float32
	stopCh   chan struct{}
	// dropped counts samples never handed to the consumer (only when it
	// didn't drain them within stopDrainTimeout of Stop); behind, samples
	// held back while it catches up.
	dropped atomic.Int64
	behind  atomic.Int64

	stateMu  sync.Mutex
	running  bool
	shut     bool
	stopping sync.Once

	windowSize int

	allMu      sync.Mutex
	allSamples []float32
}

// NewStreamCapturer wraps capturer. windowSize <= 0 means
// DefaultWindowSize; chanBuf is the window channel's buffer (negative
// counts as 0).
func NewStreamCapturer(capturer Capturer, windowSize, chanBuf int) *StreamCapturer {
	if windowSize <= 0 {
		windowSize = DefaultWindowSize
	}
	return &StreamCapturer{
		capturer:   capturer,
		windowSize: windowSize,
		windowCh:   make(chan []float32, max(chanBuf, 0)),
		stopCh:     make(chan struct{}),
	}
}

// Windows delivers the audio in windowSize pieces; it is closed once Stop
// has drained what was recorded.
func (s *StreamCapturer) Windows() <-chan []float32 { return s.windowCh }

// Start starts the device and the poll loop. A StreamCapturer runs once:
// after Stop its channel is closed for good.
func (s *StreamCapturer) Start() error {
	s.stateMu.Lock()
	switch {
	case s.shut:
		s.stateMu.Unlock()
		return errors.New("stream capturer is closed")
	case s.running:
		s.stateMu.Unlock()
		return errors.New("stream capturer already started")
	}
	s.running = true
	s.stateMu.Unlock()

	if err := s.capturer.Start(); err != nil {
		s.setRunning(false)
		return fmt.Errorf("starting capturer: %w", err)
	}
	go s.pollLoop()
	return nil
}

func (s *StreamCapturer) setRunning(v bool) {
	s.stateMu.Lock()
	s.running = v
	s.stateMu.Unlock()
}

// Stop tells the poll loop to drain and close Windows, then stops the
// device. Calling it when not running does nothing.
func (s *StreamCapturer) Stop() error {
	s.stateMu.Lock()
	wasRunning := s.running
	s.running = false
	s.stateMu.Unlock()
	if !wasRunning {
		return nil
	}

	s.stopping.Do(func() { close(s.stopCh) })
	if err := s.capturer.Stop(); err != nil {
		return fmt.Errorf("stopping capturer: %w", err)
	}
	return nil
}

// AllSamples is a copy of everything recorded so far.
func (s *StreamCapturer) AllSamples() []float32 {
	s.allMu.Lock()
	defer s.allMu.Unlock()
	return append(make([]float32, 0, len(s.allSamples)), s.allSamples...)
}

// Close stops capture if it is running and releases the device. Safe to
// call more than once.
func (s *StreamCapturer) Close() {
	s.stateMu.Lock()
	if s.shut {
		s.stateMu.Unlock()
		return
	}
	s.shut = true
	wasRunning := s.running
	s.stateMu.Unlock()

	if wasRunning {
		_ = s.Stop()
	}
	s.capturer.Close()
}

// pollLoop moves samples from the device buffer into windows on every
// tick, and on stop flushes what is left before closing the channel.
func (s *StreamCapturer) pollLoop() {
	tick := makeTickerFunc()
	defer tick.Stop()
	buf := make([]float32, 0, 2*s.windowSize)
	var pending [][]float32
	take := func(samples []float32) {
		s.allMu.Lock()
		s.allSamples = append(s.allSamples, samples...)
		s.allMu.Unlock()
		buf = append(buf, samples...)
		for len(buf) >= s.windowSize {
			window := make([]float32, s.windowSize)
			copy(window, buf[:s.windowSize])
			buf = buf[s.windowSize:]
			pending = append(pending, window)
		}
		s.behind.Store(int64(len(pending) * s.windowSize))
	}
	// send hands over held-back windows while the consumer has room.
	send := func() {
		for len(pending) > 0 {
			select {
			case s.windowCh <- pending[0]:
				pending[0] = nil
				pending = pending[1:]
				s.behind.Store(int64(len(pending) * s.windowSize))
			default:
				return
			}
		}
	}

	for {
		select {
		case <-tick.Ticks():
			samples := s.capturer.Samples()
			s.capturer.Reset()
			if len(samples) > 0 {
				take(samples)
			}
			send()

		case <-s.stopCh:
			// Drain: the remaining audio, then everything held back, waiting
			// for the consumer (it reads until the channel closes). Past a
			// deadline (a stuck consumer must not hang the stop) what's left
			// is counted in Dropped: the session's saved audio still has it.
			if samples := s.capturer.Samples(); len(samples) > 0 {
				s.capturer.Reset()
				take(samples)
			}
			deadline := time.After(stopDrainTimeout)
		drain:
			for len(pending) > 0 {
				select {
				case s.windowCh <- pending[0]:
					pending = pending[1:]
				case <-deadline:
					for _, w := range pending {
						s.dropped.Add(int64(len(w)))
					}
					break drain
				}
			}
			close(s.windowCh)
			return
		}
	}
}

// Dropped is how many samples were never handed to the consumer of
// Windows (only when it didn't finish within stopDrainTimeout of Stop).
func (s *StreamCapturer) Dropped() int64 { return s.dropped.Load() }

// Behind is how many samples are held back waiting for the consumer.
func (s *StreamCapturer) Behind() int64 { return s.behind.Load() }
