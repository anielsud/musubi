package audio

// HasSignal reports whether samples hold anything above about -80 dBFS:
// a working input picks up at least room noise, while a disabled or
// wrong device (or an app making no sound) delivers zeros. The live
// pipeline's silent-mic check uses the same bar.
func HasSignal(samples []float32) bool {
	for _, v := range samples {
		if v > 1e-4 || v < -1e-4 {
			return true
		}
	}
	return false
}
