package knowledge

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/store"
)

// Alias candidates (docs/incremental-design.md, "Entities and aliases";
// prototype: tools/entity-bench/aliases.py). Names and terms that are
// probably one thing spelled or heard two ways, proposed for the owner to
// confirm. Evidence is what any customer has: invites (attendees), the
// statements' people, and their text. Two vetoes stop the merge that does
// harm, two people with near names:
//   - both names attended one meeting;
//   - each is part of a different full name on invites.

// AliasCandidate is a proposed alias with its evidence.
type AliasCandidate struct {
	Keep, Variant           string
	Distance                int
	KeepCount, VariantCount int
	OnInvite                bool // the kept form is part of an attendee's name
	Score                   float64
}

var (
	capWord   = regexp.MustCompile(`\b[\p{Lu}][\pL]{3,}\b`)
	lowerWord = regexp.MustCompile(`\b[\p{Ll}]{4,}\b`)
)

// ProposeAliases finds candidates and records them as proposed (decided
// ones are left alone). It returns how many it proposed.
func ProposeAliases(st *store.Store, owner string) (int, error) {
	items, err := st.AllItems()
	if err != nil {
		return 0, err
	}
	sts, err := st.AllStatements()
	if err != nil {
		return 0, err
	}
	cands := AliasCandidates(items, sts, owner)
	for _, c := range cands {
		ev, _ := json.Marshal(c)
		if err := st.ProposeAlias(store.LearnedAlias{Keep: c.Keep, Variant: c.Variant, Kind: "spelling", Evidence: string(ev), Score: c.Score, By: "corpus"}); err != nil {
			return 0, err
		}
	}
	return len(cands), nil
}

// AliasCandidates computes candidates, best first.
func AliasCandidates(items []item.Item, sts []store.Statement, owner string) []AliasCandidate {
	count := map[string]int{}
	lower := map[string]int{}
	where := map[string]map[string]bool{}
	onInvite := map[string]bool{}
	tokensIn := map[string]map[string]bool{} // item → attendee name parts
	fullOf := map[string]map[string]bool{}   // name part → attendee full names
	see := func(w, item string) {
		count[w]++
		if where[w] == nil {
			where[w] = map[string]bool{}
		}
		where[w][item] = true
	}
	for _, it := range items {
		tokensIn[it.ID] = map[string]bool{}
		for _, p := range it.Participants {
			n := participantName(p)
			full := PrepNameKey(n)
			if full == "" {
				continue
			}
			for _, t := range strings.Fields(full) {
				tokensIn[it.ID][t] = true
				if fullOf[t] == nil {
					fullOf[t] = map[string]bool{}
				}
				fullOf[t][full] = true
			}
			for _, w := range strings.FieldsFunc(n, func(r rune) bool { return !unicode.IsLetter(r) }) {
				if len([]rune(w)) >= 4 {
					see(w, it.ID)
					onInvite[w] = true
				}
			}
		}
	}
	for _, s := range sts {
		for _, w := range capWord.FindAllString(s.Text, -1) {
			see(w, s.ItemID)
		}
		for _, w := range lowerWord.FindAllString(s.Text, -1) {
			lower[w]++
		}
		for _, n := range append(append([]string{}, s.Who...), s.Holder) {
			for _, w := range strings.Fields(n) {
				if len([]rune(w)) >= 4 {
					see(w, s.ItemID)
				}
			}
		}
	}
	ownerKey := strings.Fields(PrepNameKey(owner))
	ordinary := func(w string) bool { l := strings.ToLower(w); return lower[l]*5 >= lower[l]+count[w] }
	byKey := map[string][]string{}
	for w, n := range count {
		if n >= 2 && !ordinary(w) {
			byKey[soundKey(w)] = append(byKey[soundKey(w)], w)
		}
	}
	var out []AliasCandidate
	seen := map[[2]string]bool{}
	for _, ws := range byKey {
		sort.Strings(ws)
		for i, a := range ws {
			for _, b := range ws[i+1:] {
				na, nb := PrepNameKey(a), PrepNameKey(b)
				if na == nb || seen[[2]string{na, nb}] {
					continue
				}
				d := editDistance(na, nb)
				if d > 2 || (d == 2 && min(len(na), len(nb)) < 6) {
					continue
				}
				seen[[2]string{na, nb}] = true
				if contains(ownerKey, na) || contains(ownerKey, nb) {
					continue // the owner's own name: never folded into another
				}
				// Two people: met together, or parts of different full names.
				together := false
				for it := range where[a] {
					if where[b][it] && tokensIn[it][na] && tokensIn[it][nb] {
						together = true
						break
					}
				}
				distinct := len(fullOf[na]) > 0 && len(fullOf[nb]) > 0 && !shareFull(fullOf[na], fullOf[nb])
				if together || distinct {
					continue
				}
				keep, variant := a, b
				if count[b] > count[a] || (count[b] == count[a] && onInvite[b]) {
					keep, variant = b, a
				}
				score := math.Sqrt(float64(count[variant])) / float64(1+d)
				if onInvite[keep] {
					score *= 1.5
				}
				out = append(out, AliasCandidate{Keep: keep, Variant: variant, Distance: d, KeepCount: count[keep], VariantCount: count[variant],
					OnInvite: onInvite[keep], Score: math.Round(score*100) / 100})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// shareFull: two sets of full names with one in common, or sharing a
// surname (one person, written two ways).
func shareFull(a, b map[string]bool) bool {
	for x := range a {
		if b[x] {
			return true
		}
		xs := strings.Fields(x)
		for y := range b {
			ys := strings.Fields(y)
			if len(xs) > 1 && len(ys) > 1 && xs[len(xs)-1] == ys[len(ys)-1] {
				return true
			}
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// soundKey is a rough sound key: the first letter, then consonant classes,
// with an "h" after a consonant dropped (a misheard "Dhaka" and "Daka").
func soundKey(w string) string {
	s := PrepNameKey(w)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "ph", "f")
	if s == "" {
		return ""
	}
	var b strings.Builder
	codes := map[rune]byte{'b': '1', 'f': '1', 'p': '1', 'v': '1', 'c': '2', 'g': '2', 'j': '2', 'k': '2', 'q': '2', 's': '2', 'x': '2', 'z': '2',
		'd': '3', 't': '3', 'l': '4', 'm': '5', 'n': '5', 'r': '6'}
	rs := []rune(s)
	first := rs[0]
	if c, ok := codes[first]; ok {
		b.WriteByte(c)
	} else {
		b.WriteRune(first)
	}
	last := byte(0)
	for i, r := range rs[1:] {
		if r == 'h' && i >= 0 {
			continue
		}
		c, ok := codes[r]
		if !ok {
			last = 0
			continue
		}
		if c != last {
			b.WriteByte(c)
		}
		last = c
	}
	k := b.String()
	if len(k) > 5 {
		k = k[:5]
	}
	return k
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > 2 || d < -2 {
		return 9
	}
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
