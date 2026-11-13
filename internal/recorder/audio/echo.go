package audio

import (
	"math"
	"slices"
	"time"
)

// Echo matching: whether what a microphone hears is just the meeting
// audio coming back out of the speakers. A webcam or laptop mic in the
// same room as the speakers picks up the far side of the call, so its
// loudness rises and falls with the system audio a moment later; the
// owner's own voice, or any other sound in the room, does not follow it.
//
// Loudness envelopes rather than the waveforms: the speaker, the room
// and the mic change a waveform beyond recognition (filtering, reverb,
// the mic's own processing), but the rhythm of speech, syllables
// starting and stopping, survives all three and costs a few hundred
// numbers to compare.

const (
	// EchoFrame is the envelope's frame: 20 ms, a fraction of a syllable,
	// so speech rhythm is kept and the comparison stays tiny (75 frames
	// for a 1.5 s probe).
	EchoFrame = CaptureSampleRate / 50
	// EchoActiveRMS is the frame loudness (about -50 dBFS) the system
	// audio must reach somewhere in the window to count as playing
	// something: below it the speakers had nothing to echo.
	EchoActiveRMS = 0.003
	// echoBurst is how far above its own floor (the median frame) a
	// probe's loudest frame must be to count as hearing an event, a word
	// or a knock, rather than steady room noise: 12 dB.
	echoBurst = 4.0
)

// EchoMatch is how well a probe's envelope follows the reference's.
type EchoMatch struct {
	// Corr is the best Pearson correlation of the two envelopes over the
	// lags searched, -1..1 (0 when either is flat).
	Corr float64
	// Lag is how far the probe trails the reference at that best match.
	Lag time.Duration
	// RefActive: the reference reached EchoActiveRMS (it played
	// something to echo). ProbeEvents: the probe heard more than steady
	// noise.
	RefActive, ProbeEvents bool
}

// MatchEcho compares probe with ref, a longer stretch of the reference
// that starts refLead samples before the probe's first sample, by
// sliding the probe's envelope along ref's: every lag compares the whole
// probe, so a long lag is judged on as much sound as a short one. ref
// shorter than probe gives the zero EchoMatch.
func MatchEcho(probe, ref []float32, refLead int) EchoMatch {
	p, r := Envelope(probe), Envelope(ref)
	m := EchoMatch{RefActive: slices.Max(append(r, 0)) >= EchoActiveRMS, ProbeEvents: hasEvents(p)}
	if len(p) < 2 || len(r) < len(p) {
		return m
	}
	m.Corr = -2
	for off := 0; off+len(p) <= len(r); off++ {
		c := pearson(p, r[off:off+len(p)])
		if c > m.Corr {
			m.Corr = c
			m.Lag = time.Duration(refLead-off*EchoFrame) * time.Second / CaptureSampleRate
		}
	}
	return m
}

// Envelope is x's loudness, the RMS of each whole EchoFrame.
func Envelope(x []float32) []float64 {
	out := make([]float64, len(x)/EchoFrame)
	for i := range out {
		var sum float64
		for _, v := range x[i*EchoFrame : (i+1)*EchoFrame] {
			sum += float64(v) * float64(v)
		}
		out[i] = math.Sqrt(sum / EchoFrame)
	}
	return out
}

// hasEvents reports whether env's loudest frame stands echoBurst above
// its median frame.
func hasEvents(env []float64) bool {
	if len(env) == 0 {
		return false
	}
	s := slices.Sorted(slices.Values(env))
	med := s[len(s)/2]
	return s[len(s)-1] >= EchoActiveRMS/10 && s[len(s)-1] >= echoBurst*med
}

// pearson is the correlation of a and b (equal lengths); 0 if either is
// flat, where a correlation says nothing.
func pearson(a, b []float64) float64 {
	n := float64(len(a))
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma, mb = ma/n, mb/n
	var sab, saa, sbb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		saa += da * da
		sbb += db * db
	}
	if saa < 1e-18 || sbb < 1e-18 {
		return 0
	}
	return sab / math.Sqrt(saa*sbb)
}
