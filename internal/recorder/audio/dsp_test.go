package audio

import (
	"math"
	"testing"
)

// tone is n samples of a sine at hz, sampled at rate, with peak amp.
func tone(n, rate int, hz, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

// tailRMS is the RMS of the second half of x, once a filter has settled.
func tailRMS(x []float32) float64 {
	var sum float64
	tail := x[len(x)/2:]
	for _, v := range tail {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(tail)))
}

func mean(x []float32) float64 {
	var sum float64
	for _, v := range x {
		sum += float64(v)
	}
	return sum / float64(len(x))
}

func TestRemoveDCOffsetCentresOnZero(t *testing.T) {
	for _, in := range [][]float32{{0.25, 0.75, 1.25, 1.75}, {-3, -2.5, -4}, {-0.5, 0.5}} {
		out := RemoveDCOffset(in)
		if m := mean(out); math.Abs(m) > 1e-6 {
			t.Errorf("RemoveDCOffset(%v) has mean %g, want 0", in, m)
		}
		// The shape is untouched: neighbours keep their distance.
		if d0, d1 := in[1]-in[0], out[1]-out[0]; math.Abs(float64(d0-d1)) > 1e-6 {
			t.Errorf("RemoveDCOffset(%v) changed a step from %v to %v", in, d0, d1)
		}
	}
	if out := RemoveDCOffset(nil); out != nil {
		t.Errorf("RemoveDCOffset(nil) = %v, want nil", out)
	}
}

func TestHighPassFilterSplitsAtCutoff(t *testing.T) {
	const rate = 16000
	hum := tone(rate, rate, 30, 1)
	if r := tailRMS(HighPassFilter(hum, rate, 80)) / tailRMS(hum); r > 0.5 {
		t.Errorf("30 Hz kept %.2f of its level through an 80 Hz high-pass, want under 0.5", r)
	}
	voice := tone(rate, rate, 2000, 1)
	if r := tailRMS(HighPassFilter(voice, rate, 80)) / tailRMS(voice); r < 0.97 {
		t.Errorf("2 kHz kept only %.3f of its level, want nearly all", r)
	}
}

func TestHighPassFilterPassesThroughWhenItCannotFilter(t *testing.T) {
	src := []float32{0.4, -0.1, 0.25}
	for _, c := range []struct {
		rate   int
		cutoff float32
	}{{0, 80}, {-16000, 80}, {16000, 0}, {16000, -5}} {
		out := HighPassFilter(src, c.rate, c.cutoff)
		if &out[0] != &src[0] {
			t.Errorf("rate %d cutoff %v: got a new slice, want the input back", c.rate, c.cutoff)
		}
	}
	if out := HighPassFilter(nil, 16000, 80); out != nil {
		t.Errorf("HighPassFilter(nil) = %v, want nil", out)
	}
}

func TestNormalizeScalesPeakToOne(t *testing.T) {
	in := []float32{0.1, -0.25, 0.05}
	out := Normalize(in)
	want := []float32{0.4, -1, 0.2}
	for i := range want {
		if math.Abs(float64(out[i]-want[i])) > 1e-6 {
			t.Fatalf("Normalize(%v) = %v, want %v", in, out, want)
		}
	}
	if in[1] != -0.25 {
		t.Error("Normalize changed its input")
	}

	quiet := []float32{0, 0}
	if out := Normalize(quiet); &out[0] != &quiet[0] {
		t.Error("silence should come back as is")
	}
	if out := Normalize(nil); out != nil {
		t.Errorf("Normalize(nil) = %v, want nil", out)
	}
}

func TestNoiseGateKeepsOnlyLoudEnoughSamples(t *testing.T) {
	// -20 dB is 0.1 of full scale; the threshold itself is kept.
	in := []float32{0.05, -0.2, 0.1, -0.099, 1}
	got := NoiseGate(in, -20)
	want := []float32{0, -0.2, 0.1, 0, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NoiseGate(%v, -20) = %v, want %v", in, got, want)
		}
	}
	// At 0 dB only full-scale samples survive.
	if got := NoiseGate([]float32{0.999, -1}, 0); got[0] != 0 || got[1] != -1 {
		t.Errorf("NoiseGate at 0 dB = %v, want [0 -1]", got)
	}
	if got := NoiseGate(nil, -40); got != nil {
		t.Errorf("NoiseGate(nil) = %v, want nil", got)
	}
}

func TestProcessPipelineCleansAndNormalises(t *testing.T) {
	const rate = 16000
	in := tone(rate, rate, 300, 0.4)
	for i := range in {
		in[i] += 0.25
	}
	out := ProcessPipeline(in, rate, -40)
	if len(out) != len(in) {
		t.Fatalf("length %d, want %d", len(out), len(in))
	}
	var peak float32
	for _, v := range out[rate/2:] {
		peak = max(peak, absf32(v))
	}
	if peak < 0.9 || peak > 1.0001 {
		t.Errorf("settled peak %v, want about 1", peak)
	}
	if m := mean(out[rate/2:]); math.Abs(m) > 0.01 {
		t.Errorf("settled mean %v, want the offset gone", m)
	}
}

func TestProcessPipelineGateOnlyWhenNegative(t *testing.T) {
	const rate = 16000
	// A loud tone with a faint stretch: gated at -20 dB the faint part is
	// silenced; with gateDB 0 it must still be there.
	in := tone(rate, rate, 500, 1)
	for i := rate / 2; i < rate/2+400; i++ {
		in[i] *= 0.01
	}
	faint := func(out []float32) bool {
		for _, v := range out[rate/2+100 : rate/2+300] {
			if v != 0 {
				return true
			}
		}
		return false
	}
	if !faint(ProcessPipeline(in, rate, 0)) {
		t.Error("gateDB 0 gated the faint stretch")
	}
	if faint(ProcessPipeline(in, rate, -20)) {
		t.Error("gateDB -20 left the faint stretch in")
	}
}

func TestResamplerPiecesMatchOneCall(t *testing.T) {
	for _, rr := range [][2]int{{48000, 16000}, {44100, 16000}, {11025, 16000}} {
		src, dst := rr[0], rr[1]
		in := tone(src*2, src, 523, 0.6)
		for i, v := range tone(src*2, src, 2900, 0.3) {
			in[i] += v
		}
		oneCall := Resample(in, src, dst)

		rs := NewResampler(src, dst)
		var pieces []float32
		sizes := []int{1, 2, 511, 1024, 37, 4096}
		for off, k := 0, 0; off < len(in); k++ {
			end := min(off+sizes[k%len(sizes)], len(in))
			pieces = append(pieces, rs.Process(in[off:end])...)
			off = end
		}

		// Only what needs input after the last sample may be missing.
		short := len(oneCall) - len(pieces)
		if short < 0 || short > (dst+src-1)/src {
			t.Fatalf("%d->%d: %d samples in pieces vs %d in one call", src, dst, len(pieces), len(oneCall))
		}
		for i, v := range pieces {
			if math.Abs(float64(v-oneCall[i])) > 1e-4 {
				t.Fatalf("%d->%d: sample %d is %v in pieces, %v in one call", src, dst, i, v, oneCall[i])
			}
		}
	}
}

func TestResamplerKeepsTimeOverLongStreams(t *testing.T) {
	// Half an hour of 48 kHz callbacks of 960 samples (20 ms): the output
	// must stay within a sample of a third of the input.
	const callbacks = 30 * 60 * 50
	down := NewResampler(48000, 16000)
	piece := make([]float32, 960)
	n := 0
	for i := 0; i < callbacks; i++ {
		n += len(down.Process(piece))
	}
	if want := callbacks * 960 / 3; n < want-1 || n > want {
		t.Errorf("%d output samples, want %d (or one fewer)", n, want)
	}
}

func TestResamplerEqualRatesIsIdentity(t *testing.T) {
	in := []float32{-0.5, 0, 0.5}
	same := NewResampler(16000, 16000)
	if out := same.Process(in); len(out) != len(in) || &out[0] != &in[0] {
		t.Errorf("Process = %v, want the input slice back", out)
	}
}

func TestLowPassFilter(t *testing.T) {
	const sampleRate = 16000

	t.Run("attenuates high frequency", func(t *testing.T) {
		// Generate 4000Hz sine wave (well above 1000Hz cutoff)
		n := sampleRate
		samples := make([]float32, n)
		for i := range samples {
			samples[i] = float32(math.Sin(2 * math.Pi * 4000 * float64(i) / float64(sampleRate)))
		}

		result := LowPassFilter(samples, sampleRate, 1000)

		var inputRMS, outputRMS float64
		half := n / 2
		for i := half; i < n; i++ {
			inputRMS += float64(samples[i]) * float64(samples[i])
			outputRMS += float64(result[i]) * float64(result[i])
		}
		inputRMS = math.Sqrt(inputRMS / float64(n-half))
		outputRMS = math.Sqrt(outputRMS / float64(n-half))

		ratio := outputRMS / inputRMS
		if ratio > 0.5 {
			t.Errorf("4000Hz attenuation ratio = %f, want < 0.5", ratio)
		}
	})

	t.Run("passes low frequency", func(t *testing.T) {
		// Generate 100Hz sine wave (well below 1000Hz cutoff)
		n := sampleRate
		samples := make([]float32, n)
		for i := range samples {
			samples[i] = float32(math.Sin(2 * math.Pi * 100 * float64(i) / float64(sampleRate)))
		}

		result := LowPassFilter(samples, sampleRate, 1000)

		var inputRMS, outputRMS float64
		half := n / 2
		for i := half; i < n; i++ {
			inputRMS += float64(samples[i]) * float64(samples[i])
			outputRMS += float64(result[i]) * float64(result[i])
		}
		inputRMS = math.Sqrt(inputRMS / float64(n-half))
		outputRMS = math.Sqrt(outputRMS / float64(n-half))

		ratio := outputRMS / inputRMS
		if ratio < 0.9 {
			t.Errorf("100Hz pass-through ratio = %f, want > 0.9", ratio)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result := LowPassFilter(nil, sampleRate, 1000)
		if result != nil {
			t.Errorf("expected nil for nil input")
		}
	})

	t.Run("invalid params", func(t *testing.T) {
		samples := []float32{1, 2, 3}
		result := LowPassFilter(samples, 0, 1000)
		if len(result) != len(samples) {
			t.Errorf("expected passthrough for invalid sampleRate")
		}
	})
}

func TestResample(t *testing.T) {
	t.Run("same rate returns input unchanged", func(t *testing.T) {
		samples := []float32{1, 2, 3, 4}
		result := Resample(samples, 16000, 16000)
		if len(result) != len(samples) {
			t.Fatalf("len = %d, want %d", len(result), len(samples))
		}
		for i := range samples {
			if result[i] != samples[i] {
				t.Errorf("result[%d] = %v, want %v", i, result[i], samples[i])
			}
		}
	})

	t.Run("48kHz to 16kHz downsamples to a third the length", func(t *testing.T) {
		samples := make([]float32, 4800)
		for i := range samples {
			samples[i] = float32(i)
		}
		result := Resample(samples, 48000, 16000)
		wantLen := 1600
		if len(result) != wantLen {
			t.Fatalf("len = %d, want %d", len(result), wantLen)
		}
		// A ramp is pure DC + low-frequency trend, which the anti-alias
		// low-pass filter should pass through, not attenuate -- so once
		// the filter's transient has settled (well past the start), the
		// resampled slope should still track the expected ~3 units per
		// output sample (3x decimation of a unit-slope ramp).
		lo, hi := len(result)/2, len(result)-1
		gotSlope := (result[hi] - result[lo]) / float32(hi-lo)
		if math.Abs(float64(gotSlope)-3) > 0.05 {
			t.Errorf("settled slope = %v, want ~3", gotSlope)
		}
	})

	t.Run("anti-aliases content above the target Nyquist", func(t *testing.T) {
		// A pure tone well above 16kHz's Nyquist (8kHz) should be
		// suppressed by the anti-alias filter before decimation, not
		// aliased down into the audible band at full strength -- this
		// is the actual bug this filter fixes (see LowPassFilter and
		// Resample's doc comments): ScreenCaptureKit's guest-audio tap
		// delivers 48kHz, and without this, folded-back high-frequency
		// energy would degrade internal/speaker's embedding quality.
		const srcRate = 48000
		const dstRate = 16000
		const toneHz = 20000 // well above dstRate's 8kHz Nyquist
		n := srcRate         // 1 second
		samples := make([]float32, n)
		for i := range samples {
			samples[i] = float32(math.Sin(2 * math.Pi * toneHz * float64(i) / float64(srcRate)))
		}

		result := Resample(samples, srcRate, dstRate)

		// Measure RMS of the second half (after the filter settles).
		half := len(result) / 2
		var outRMS float64
		for i := half; i < len(result); i++ {
			outRMS += float64(result[i]) * float64(result[i])
		}
		outRMS = math.Sqrt(outRMS / float64(len(result)-half))

		// Input RMS for a unit-amplitude sine is ~0.707; a properly
		// anti-aliased 20kHz tone should come through heavily
		// attenuated, not preserved near full strength.
		if outRMS > 0.2 {
			t.Errorf("20kHz tone RMS after resample = %v, want < 0.2 (anti-aliased)", outRMS)
		}
	})

	t.Run("passes low frequency content through resampling", func(t *testing.T) {
		// Sanity check: the anti-alias filter shouldn't gut legitimate
		// low-frequency audio content along with the high-frequency
		// content it's meant to suppress.
		const srcRate = 48000
		const dstRate = 16000
		const toneHz = 440 // well below dstRate's 8kHz Nyquist
		n := srcRate
		samples := make([]float32, n)
		for i := range samples {
			samples[i] = float32(math.Sin(2 * math.Pi * toneHz * float64(i) / float64(srcRate)))
		}

		result := Resample(samples, srcRate, dstRate)

		half := len(result) / 2
		var outRMS float64
		for i := half; i < len(result); i++ {
			outRMS += float64(result[i]) * float64(result[i])
		}
		outRMS = math.Sqrt(outRMS / float64(len(result)-half))

		if outRMS < 0.5 {
			t.Errorf("440Hz tone RMS after resample = %v, want > 0.5 (should pass through)", outRMS)
		}
	})

	t.Run("upsampling increases length", func(t *testing.T) {
		samples := []float32{0, 1, 2, 3}
		result := Resample(samples, 16000, 48000)
		wantLen := 12
		if len(result) != wantLen {
			t.Fatalf("len = %d, want %d", len(result), wantLen)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result := Resample(nil, 48000, 16000)
		if result != nil {
			t.Errorf("result = %v, want nil", result)
		}
	})

	t.Run("non-positive rates return input unchanged", func(t *testing.T) {
		samples := []float32{1, 2, 3}
		if got := Resample(samples, 0, 16000); len(got) != len(samples) {
			t.Errorf("srcRateHz=0: len = %d, want %d", len(got), len(samples))
		}
		if got := Resample(samples, 48000, 0); len(got) != len(samples) {
			t.Errorf("dstRateHz=0: len = %d, want %d", len(got), len(samples))
		}
	})
}
