package knowledge

// Average-linkage agglomerative clustering with a stopping threshold, by the
// nearest-neighbour chain algorithm: O(n²) time over a condensed similarity
// matrix (n(n-1)/2 float32s: about 130 MB at 8,000 claims). Average linkage
// can't drift the way a running centroid does: two groups merge only while
// their members are similar on average.

// hac groups vectors (unit length) and returns cluster member indices.
// adjust, if set, shifts the similarity of a pair (e.g. apart for claims
// about different customers).
func hac(vs [][]float32, threshold float64, adjust func(i, j int) float32) [][]int {
	n := len(vs)
	if n == 0 {
		return nil
	}
	idx := func(i, j int) int {
		if i > j {
			i, j = j, i
		}
		return i*n - i*(i+1)/2 + (j - i - 1)
	}
	sim := make([]float32, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			v := float32(cos(vs[i], vs[j]))
			if adjust != nil {
				v += adjust(i, j)
			}
			sim[idx(i, j)] = v
		}
	}
	active := make([]bool, n)
	size := make([]int, n)
	members := make([][]int, n)
	for i := range active {
		active[i], size[i], members[i] = true, 1, []int{i}
	}
	done := make([]bool, n) // can't merge with anything at threshold
	var chain []int
	remaining := n
	for remaining > 1 {
		if len(chain) == 0 {
			for i := 0; i < n; i++ {
				if active[i] && !done[i] {
					chain = append(chain, i)
					break
				}
			}
			if len(chain) == 0 {
				break
			}
		}
		a := chain[len(chain)-1]
		best, bs := -1, float32(-2)
		for k := 0; k < n; k++ {
			if k == a || !active[k] || done[k] {
				continue
			}
			s := sim[idx(a, k)]
			if s > bs || (s == bs && len(chain) > 1 && k == chain[len(chain)-2]) {
				best, bs = k, s
			}
		}
		if best < 0 || float64(bs) < threshold {
			// Nothing is similar enough to a, now or after later merges
			// (average linkage only lowers similarity to merged groups).
			done[a] = true
			chain = chain[:len(chain)-1]
			continue
		}
		if len(chain) > 1 && chain[len(chain)-2] == best {
			// Reciprocal nearest neighbours: merge best into a.
			chain = chain[:len(chain)-2]
			for k := 0; k < n; k++ {
				if k == a || k == best || !active[k] {
					continue
				}
				s := (float32(size[a])*sim[idx(a, k)] + float32(size[best])*sim[idx(best, k)]) / float32(size[a]+size[best])
				sim[idx(a, k)] = s
			}
			size[a] += size[best]
			members[a] = append(members[a], members[best]...)
			active[best] = false
			members[best] = nil
			remaining--
			continue
		}
		chain = append(chain, best)
	}
	var out [][]int
	for i := 0; i < n; i++ {
		if active[i] {
			out = append(out, members[i])
		}
	}
	return out
}
