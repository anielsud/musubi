package speaker

import "math"

// CosineSimilarity is a·b / (|a||b|), accumulated in float64. Vectors of
// different or zero length, or with a zero norm, score 0, so a missing
// fingerprint never looks like a match.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i, x := range a {
		y := float64(b[i])
		dot += float64(x) * y
		aa += float64(x) * float64(x)
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / (math.Sqrt(aa) * math.Sqrt(bb))
}
