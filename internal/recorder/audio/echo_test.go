package audio

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

// speechLike is n samples of something with speech's rhythm: a buzzy
// voiced carrier (a random pitch with harmonics, plus breath noise) cut
// into syllables of 80-250 ms with short gaps and the odd pause, at
// peak amplitude amp. Seeded, so tests are repeatable.
func speechLike(r *rand.Rand, n int, amp float64) []float32 {
	out := make([]float32, n)
	pitch := 100 + 120*r.Float64()
	phase := 0.0
	for i := 0; i < n; {
		syl := int((0.08 + 0.17*r.Float64()) * CaptureSampleRate)
		for j := 0; j < syl && i+j < n; j++ {
			shape := math.Sin(math.Pi * float64(j) / float64(syl)) // rise and fall
			phase += 2 * math.Pi * pitch * (1 + 0.05*math.Sin(float64(i+j)/2000)) / CaptureSampleRate
			v := 0.6*math.Sin(phase) + 0.25*math.Sin(2*phase) + 0.15*math.Sin(3*phase) + 0.2*(r.Float64()*2-1)
			out[i+j] = float32(amp * shape * v)
		}
		i += syl
		gap := 0.03 + 0.15*r.Float64()
		if r.Float64() < 0.15 {
			gap = 0.3 + 0.5*r.Float64()
		}
		i += int(gap * CaptureSampleRate)
	}
	return out
}

// roomEcho is x as a mic across the room hears it from the speakers:
// delayed, attenuated by gain, smeared by a reverb tail decaying 60 dB
// in rt60, with room noise at noise RMS.
func roomEcho(r *rand.Rand, x []float32, delay time.Duration, gain float64, rt60 time.Duration, noise float64) []float32 {
	d := int(delay.Seconds() * CaptureSampleRate)
	irLen := int(rt60.Seconds() * CaptureSampleRate)
	// A sparse reverb tail: enough taps to smear the envelope like a
	// room does, few enough to keep the test quick.
	type tap struct {
		at int
		g  float64
	}
	taps := []tap{{0, 1}}
	for k := 0; k < 60; k++ {
		at := 1 + r.IntN(irLen)
		taps = append(taps, tap{at, 0.5 * math.Pow(10, -3*float64(at)/float64(irLen)) * (r.Float64()*2 - 1)})
	}
	out := make([]float32, len(x))
	for i := range out {
		var v float64
		for _, t := range taps {
			if j := i - d - t.at; j >= 0 && j < len(x) {
				v += t.g * float64(x[j])
			}
		}
		out[i] = float32(gain*v + noise*r.NormFloat64())
	}
	return out
}

func add(a, b []float32) []float32 {
	out := make([]float32, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out
}

// probeAndRef cuts a probe of probeFor out of mic, and the stretch of
// sys from lead before it to trail after it, as the source check does.
func probeAndRef(mic, sys []float32, at int, probeFor, lead, trail time.Duration) ([]float32, []float32, int) {
	n := int(probeFor.Seconds() * CaptureSampleRate)
	l := int(lead.Seconds() * CaptureSampleRate)
	t := int(trail.Seconds() * CaptureSampleRate)
	return mic[at : at+n], sys[at-l : at+n+t], l
}

func TestMatchEcho(t *testing.T) {
	const probe, lead, trail = 1500 * time.Millisecond, 600 * time.Millisecond, 300 * time.Millisecond
	r := rand.New(rand.NewPCG(1, 2))
	sys := speechLike(r, 10*CaptureSampleRate, 0.3)
	at := 3 * CaptureSampleRate

	t.Run("echo", func(t *testing.T) {
		mic := roomEcho(r, sys, 200*time.Millisecond, 0.1, 400*time.Millisecond, 0.002)
		m := MatchEcho(probeAndRef(mic, sys, at, probe, lead, trail))
		if !m.RefActive || m.Corr < 0.8 {
			t.Errorf("echo: %+v", m)
		}
		if d := m.Lag - 200*time.Millisecond; d < -40*time.Millisecond || d > 40*time.Millisecond {
			t.Errorf("lag %v, want about 200ms", m.Lag)
		}
	})
	t.Run("own voice", func(t *testing.T) {
		mic := speechLike(r, len(sys), 0.05)
		m := MatchEcho(probeAndRef(mic, sys, at, probe, lead, trail))
		if !m.ProbeEvents || m.Corr > 0.5 {
			t.Errorf("own voice: %+v", m)
		}
	})
	t.Run("speakers silent", func(t *testing.T) {
		mic := speechLike(r, len(sys), 0.05)
		m := MatchEcho(probeAndRef(mic, make([]float32, len(sys)), at, probe, lead, trail))
		if m.RefActive || m.Corr != 0 || !m.ProbeEvents {
			t.Errorf("speakers silent: %+v", m)
		}
	})
	t.Run("steady noise", func(t *testing.T) {
		mic := roomEcho(r, make([]float32, len(sys)), 0, 0, 100*time.Millisecond, 0.002)
		if m := MatchEcho(probeAndRef(mic, sys, at, probe, lead, trail)); m.ProbeEvents {
			t.Errorf("steady noise has events: %+v", m)
		}
	})
	t.Run("short ref", func(t *testing.T) {
		if m := MatchEcho(sys[:CaptureSampleRate], sys[:EchoFrame*10], 0); m.Corr != 0 {
			t.Errorf("short ref: %+v", m)
		}
	})
}
