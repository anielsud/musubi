package audio

import "math"

// RemoveDCOffset returns samples shifted so their mean is zero. The mean
// is summed in float64: over minutes of audio a float32 sum loses the
// small bias it is meant to find.
func RemoveDCOffset(samples []float32) []float32 {
	if len(samples) == 0 {
		return samples
	}
	var total float64
	for _, v := range samples {
		total += float64(v)
	}
	bias := float32(total / float64(len(samples)))
	out := make([]float32, len(samples))
	for i := range samples {
		out[i] = samples[i] - bias
	}
	return out
}

// onePoleRC is the RC time constant and sample period of a one-pole
// filter with the given cutoff, the two terms both IIR filters here are
// built from.
func onePoleRC(sampleRate int, cutoffHz float32) (rc, dt float64) {
	return 1 / (2 * math.Pi * float64(cutoffHz)), 1 / float64(sampleRate)
}

// HighPassFilter is a one-pole IIR high-pass at cutoffHz, used at 80 Hz
// to take out rumble, mains hum and breath pops before VAD. It returns
// samples as they are when there is nothing to do or the rate or cutoff
// is not positive.
func HighPassFilter(samples []float32, sampleRate int, cutoffHz float32) []float32 {
	if len(samples) == 0 || sampleRate <= 0 || cutoffHz <= 0 {
		return samples
	}
	rc, dt := onePoleRC(sampleRate, cutoffHz)
	a := float32(rc / (rc + dt))

	out := make([]float32, len(samples))
	prevIn, prevOut := samples[0], samples[0]
	out[0] = prevOut
	for i := 1; i < len(samples); i++ {
		x := samples[i]
		prevOut = a * (prevOut + x - prevIn)
		out[i] = prevOut
		prevIn = x
	}
	return out
}

// absf32 is |v| without leaving float32.
func absf32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// Normalize scales samples so the loudest one sits at ±1. Silence comes
// back as it is, since there is no peak to scale by.
func Normalize(samples []float32) []float32 {
	var peak float32
	for _, v := range samples {
		if a := absf32(v); a > peak {
			peak = a
		}
	}
	if peak == 0 {
		return samples
	}
	out := make([]float32, len(samples))
	for i, v := range samples {
		out[i] = v / peak
	}
	return out
}

// LowPassFilter applies a single-pole IIR low-pass filter, complementing
// HighPassFilter above. A single pass only rolls off gently
// (-6dB/octave) — Resample below cascades several passes to get a
// steeper effective cutoff, since it needs this as an anti-aliasing
// filter before decimating, not just gentle tone shaping.
func LowPassFilter(samples []float32, sampleRate int, cutoffHz float32) []float32 {
	if len(samples) == 0 || sampleRate <= 0 || cutoffHz <= 0 {
		return samples
	}

	// Single-pole IIR: y[n] = y[n-1] + alpha * (x[n] - y[n-1])
	// alpha = dt / (RC + dt), where RC = 1/(2*pi*cutoff), dt = 1/sampleRate
	rc := 1.0 / (2.0 * math.Pi * float64(cutoffHz))
	dt := 1.0 / float64(sampleRate)
	alpha := float32(dt / (rc + dt))

	out := make([]float32, len(samples))
	out[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		out[i] = out[i-1] + alpha*(samples[i]-out[i-1])
	}
	return out
}

// NoiseGate silences every sample quieter than thresholdDB below full
// scale (-40 dB is about 0.01) and keeps the rest untouched, so steady
// background hiss doesn't keep VAD awake.
func NoiseGate(samples []float32, thresholdDB float32) []float32 {
	if len(samples) == 0 {
		return samples
	}
	floor := float32(math.Pow(10, float64(thresholdDB)/20.0))
	out := make([]float32, len(samples))
	for i, v := range samples {
		if absf32(v) >= floor {
			out[i] = v
		}
	}
	return out
}

// antiAliasPasses is how many cascaded LowPassFilter passes Resample
// applies before decimating. One pole only rolls off -6dB/octave, too
// gentle to be a real anti-aliasing filter on its own; cascading a few
// gets a steeper effective cutoff without a more complex filter design.
const antiAliasPasses = 4

// Resample converts samples captured at srcRateHz to dstRateHz using
// linear interpolation, anti-aliased with LowPassFilter when
// downsampling. Used by sources that don't natively capture at
// CaptureSampleRate (e.g. guestaudio's ScreenCaptureKit tap, delivering
// 48kHz) — every downstream consumer (VAD, Parakeet TDT, and critically
// internal/speaker's embedding model) assumes its input already matches
// CaptureSampleRate, since nothing else in this pipeline resamples.
//
// The anti-alias step matters beyond general audio quality: without it,
// frequency content above the new Nyquist limit folds back into the
// audible range as aliasing noise on decimation, which measurably hurts
// speaker-embedding quality — the reason this exists at all is that
// Linux's PulseAudio monitor path captures natively at 16kHz and never
// resamples, so without this, only macOS's speaker clustering would be
// working from degraded input.
//
// Returns samples unchanged if the rates already match or either rate
// is non-positive.
func Resample(samples []float32, srcRateHz, dstRateHz int) []float32 {
	if len(samples) == 0 || srcRateHz <= 0 || dstRateHz <= 0 || srcRateHz == dstRateHz {
		return samples
	}

	if srcRateHz > dstRateHz {
		// Cutoff below the target Nyquist (dstRateHz/2), leaving margin
		// since a single-pole filter's rolloff isn't a brick wall.
		cutoff := float32(dstRateHz) * 0.45
		for i := 0; i < antiAliasPasses; i++ {
			samples = LowPassFilter(samples, srcRateHz, cutoff)
		}
	}

	ratio := float64(srcRateHz) / float64(dstRateHz)
	outLen := int(float64(len(samples)) / ratio)
	if outLen <= 0 {
		return nil
	}

	out := make([]float32, outLen)
	for i := 0; i < outLen; i++ {
		srcPos := float64(i) * ratio
		idx := int(srcPos)
		if idx >= len(samples) {
			idx = len(samples) - 1
		}
		frac := float32(srcPos - float64(idx))

		if idx+1 < len(samples) {
			out[i] = samples[idx] + (samples[idx+1]-samples[idx])*frac
		} else {
			out[i] = samples[idx]
		}
	}
	return out
}

// Resampler does what Resample does, for a stream that arrives in pieces
// (capture callbacks). Resampling each piece on its own would restart the
// anti-alias filters at every boundary, which clicks, and would round
// away each piece's fractional last output sample, so the output would
// drift behind real time (about 1 ms a second for 1024-sample pieces at
// 48 kHz). Resampler keeps the filter state and the read position
// between calls, so the pieces join up to the same output as one call
// over the whole stream. One goroutine at a time.
type Resampler struct {
	from, to int
	step     float64 // input samples per output sample

	// k is the anti-alias filters' coefficient, 0 when not downsampling;
	// stage holds each cascaded filter's last output once primed.
	k      float32
	stage  [antiAliasPasses]float32
	primed bool

	// tail is filtered input still needed for interpolation; at is the
	// next output sample's position in it.
	tail []float32
	at   float64
}

// NewResampler returns a Resampler from srcRateHz to dstRateHz.
func NewResampler(srcRateHz, dstRateHz int) *Resampler {
	r := &Resampler{from: srcRateHz, to: dstRateHz}
	if srcRateHz > 0 && dstRateHz > 0 {
		r.step = float64(srcRateHz) / float64(dstRateHz)
	}
	if dstRateHz > 0 && srcRateHz > dstRateHz {
		// The cutoff Resample uses, rounded through float32 as it is there.
		rc, dt := onePoleRC(srcRateHz, float32(dstRateHz)*0.45)
		r.k = float32(dt / (rc + dt))
	}
	return r
}

// filter runs one input sample through the cascaded low-pass filters.
// The first sample primes every stage with itself, as LowPassFilter
// starts from its first input.
func (r *Resampler) filter(x float32) float32 {
	for i := range r.stage {
		if r.primed {
			r.stage[i] += r.k * (x - r.stage[i])
		} else {
			r.stage[i] = x
		}
		x = r.stage[i]
	}
	r.primed = true
	return x
}

// Process resamples the next piece of the stream. Interpolating past the
// newest input sample needs the one after it, so output trails input by
// up to one sample until more arrives.
func (r *Resampler) Process(samples []float32) []float32 {
	if len(samples) == 0 || r.from <= 0 || r.to <= 0 || r.from == r.to {
		return samples
	}

	if r.k > 0 {
		for _, x := range samples {
			r.tail = append(r.tail, r.filter(x))
		}
	} else {
		r.tail = append(r.tail, samples...)
	}

	var out []float32
	for i := int(r.at); i+1 < len(r.tail); i = int(r.at) {
		f := float32(r.at - float64(i))
		out = append(out, r.tail[i]+(r.tail[i+1]-r.tail[i])*f)
		r.at += r.step
	}

	// Drop the input every later output sample is past.
	used := min(int(r.at), len(r.tail))
	r.tail = r.tail[:copy(r.tail, r.tail[used:])]
	r.at -= float64(used)
	return out
}

// ProcessPipeline cleans up speech for VAD and recognition: DC offset
// out, 80 Hz high-pass, peak-normalise, then, when gateDB is negative,
// the noise gate. gateDB of 0 or above leaves the gate off.
func ProcessPipeline(samples []float32, sampleRate int, gateDB float32) []float32 {
	out := Normalize(HighPassFilter(RemoveDCOffset(samples), sampleRate, 80))
	if gateDB < 0 {
		out = NoiseGate(out, gateDB)
	}
	return out
}
