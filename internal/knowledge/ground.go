package knowledge

import (
	"regexp"
	"strings"
)

// Ground checks a statement against the lines it came from, without a
// model: the rewrite into a standalone sentence is where meaning drifts
// (a negation dropped, a number changed, a garbled line made confident),
// and these three checks catch the commonest cases cheaply.
//
//   - the quote must be in the cited line (or a neighbour: the model is
//     sometimes a line off);
//   - every number in the statement must appear in that source;
//   - negation must agree: the source says "not" and the statement doesn't,
//     or the other way round.
//
// A statement failing any check is kept but marked ungrounded: it counts
// for less in belief and never joins a claim without the judge.
func Ground(e Extracted, lines []Line) (bool, []string) {
	if len(lines) == 0 {
		return false, []string{"no source"}
	}
	i := e.Line - 1
	var window []string
	for k := i - 1; k <= i+1; k++ {
		if k >= 0 && k < len(lines) {
			window = append(window, lines[k].Text)
		}
	}
	source := strings.Join(window, " ")
	var issues []string

	srcWords := wordSet(source)
	qw := plainWords(e.Quote)
	if len(qw) < 2 {
		issues = append(issues, "no quote")
	} else {
		hit := 0
		for _, w := range qw {
			if srcWords[w] {
				hit++
			}
		}
		if float64(hit) < 0.7*float64(len(qw)) {
			issues = append(issues, "quote not in source")
		}
	}

	srcNums := numbersIn(source)
	for n := range numbersIn(e.Text) {
		if !srcNums[n] {
			issues = append(issues, "figure not in source: "+n)
			break
		}
	}

	// Negation is judged on the quoted words when they were found, else on
	// the cited line: a negation elsewhere in the window may be about
	// something else.
	basis := e.Quote
	if len(issues) > 0 || len(qw) < 2 {
		basis = lines[i].Text
	}
	if negatedOrContrast(basis) != negatedOrContrast(e.Text) {
		issues = append(issues, "negation differs from source")
	}
	return len(issues) == 0, issues
}

func plainWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'') })
}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range plainWords(s) {
		out[w] = true
	}
	return out
}

var reDigits = regexp.MustCompile(`\d+(?:[.,]\d+)*`)

// numbersIn returns the numbers a text states, as digit strings, reading
// spelled-out numbers too ("forty five" and "45" are the same figure).
func numbersIn(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reDigits.FindAllString(s, -1) {
		out[strings.ReplaceAll(m, ",", "")] = true
	}
	ws := plainWords(s)
	for i := 0; i < len(ws); i++ {
		if _, ok := numberWords[ws[i]]; !ok || ws[i] == "a" {
			continue
		}
		j := i
		for j < len(ws) {
			_, nw := numberWords[ws[j]]
			_, sw := scaleWords[ws[j]]
			if !(nw || sw || ws[j] == "and") || ws[j] == "a" {
				break
			}
			j++
		}
		if n, ok := parseNumber(strings.Join(ws[i:j], " ")); ok {
			// formatFloat is already the shortest form; trimming zeros
			// from it turned "forty" into "4" (fixed 2026-10-12).
			out[formatFloat(n)] = true
		}
		i = j
	}
	return out
}

// negatedOrContrast also counts a contrast ("three years instead of two")
// as the statement form of "three years, not two", so a faithful rewrite
// of a contrast isn't flagged as a dropped negation.
func negatedOrContrast(s string) bool {
	l := strings.ToLower(s)
	return negated(s) || strings.Contains(l, "instead of") || strings.Contains(l, "rather than")
}
