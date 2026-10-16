package knowledge

import (
	"math"
	"sort"

	"github.com/anielsud/musubi/internal/store"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Value is a statement's typed value (store.Value): the words as said and
// bounds normalised from them here, deterministically, so the model has
// one less place to drift. Lo/Hi bound a day, a month, a quarter, or a single
// number (Lo == Hi).
type Value = store.Value

// Compare says how two values relate: "same" (equal), "within" (a lies
// inside b: November within Q4), "contains" (b inside a), "overlaps",
// "differs" (disjoint), or "unknown" (types differ or either unread).
func Compare(a, b Value) string {
	if a.Type == "none" || b.Type == "none" || a.Type == "" || b.Type == "" {
		return "unknown"
	}
	if a.Type != b.Type || !a.Normalized || !b.Normalized {
		return "unknown"
	}
	eq := func(x, y float64) bool {
		if a.Type == "date" {
			return math.Abs(x-y) < 1
		}
		return math.Abs(x-y) <= 0.005*math.Max(math.Abs(x), math.Abs(y))
	}
	switch {
	case eq(a.Lo, b.Lo) && eq(a.Hi, b.Hi):
		return "same"
	case a.Lo >= b.Lo && a.Hi <= b.Hi:
		return "within"
	case b.Lo >= a.Lo && b.Hi <= a.Hi:
		return "contains"
	case a.Hi < b.Lo || b.Hi < a.Lo:
		return "differs"
	}
	return "overlaps"
}

// Normalize reads the raw words of a value, relative to when it was said.
func Normalize(typ, raw string, said time.Time) Value {
	v := Value{Type: strings.ToLower(strings.TrimSpace(typ)), Raw: strings.TrimSpace(raw)}
	if v.Raw == "" {
		v.Type = "none"
	}
	switch v.Type {
	case "date":
		if lo, hi, ok := parseDate(v.Raw, said); ok {
			v.Lo, v.Hi, v.Normalized = float64(lo.Unix()), float64(hi.Unix()), true
		}
	case "number", "money", "percent":
		if n, ok := parseNumber(v.Raw); ok {
			v.Lo, v.Hi, v.Normalized = n, n, true
		}
	case "duration":
		if d, ok := parseDuration(v.Raw); ok {
			v.Lo, v.Hi, v.Normalized = d, d, true
		}
	default:
		v.Type = "none"
	}
	return v
}

var months = map[string]time.Month{
	"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3, "apr": 4, "april": 4, "may": 5,
	"jun": 6, "june": 6, "jul": 7, "july": 7, "aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9,
	"oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12,
}

var weekdays = map[string]time.Weekday{
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6,
}

var (
	reISO     = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})`)
	reQuarter = regexp.MustCompile(`\bq([1-4])\b(?:\s*(?:of\s*)?(\d{4}))?`)
	reHalf    = regexp.MustCompile(`\bh([12])\b(?:\s*(\d{4}))?`)
	reYear    = regexp.MustCompile(`\b(20\d{2})\b`)
	reDayNum  = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\b`)
)

func dateOf(y int, m time.Month, d int, loc *time.Location) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// parseDate returns the period the words name: a day, a month, a quarter,
// a half, a week; or false.
func parseDate(raw string, said time.Time) (time.Time, time.Time, bool) {
	s := strings.ToLower(raw)
	loc := said.Location()
	if loc == nil {
		loc = time.UTC
	}
	year := said.Year()
	if m := reYear.FindStringSubmatch(s); m != nil {
		year, _ = strconv.Atoi(m[1])
	}
	// A month or quarter earlier than now, with no year, means next year.
	future := func(t time.Time) time.Time {
		if reYear.FindString(s) == "" && t.Before(dateOf(said.Year(), said.Month(), 1, loc).AddDate(0, -2, 0)) {
			return t.AddDate(1, 0, 0)
		}
		return t
	}
	if m := reISO.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		t := dateOf(y, time.Month(mo), d, loc)
		return t, t.AddDate(0, 0, 1), true
	}
	if m := reQuarter.FindStringSubmatch(s); m != nil {
		q, _ := strconv.Atoi(m[1])
		y := year
		if m[2] != "" {
			y, _ = strconv.Atoi(m[2])
		}
		lo := future(dateOf(y, time.Month(3*(q-1)+1), 1, loc))
		return lo, lo.AddDate(0, 3, 0), true
	}
	if m := reHalf.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		y := year
		if m[2] != "" {
			y, _ = strconv.Atoi(m[2])
		}
		lo := future(dateOf(y, time.Month(6*(h-1)+1), 1, loc))
		return lo, lo.AddDate(0, 6, 0), true
	}
	words := strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	for i, w := range words {
		if mo, ok := months[w]; ok {
			// "September 22", "22 September", or the whole month.
			d := 0
			if i+1 < len(words) {
				if n, err := strconv.Atoi(strings.TrimRight(words[i+1], "stndrh")); err == nil && n >= 1 && n <= 31 {
					d = n
				}
			}
			if d == 0 && i > 0 {
				if n, err := strconv.Atoi(strings.TrimRight(words[i-1], "stndrh")); err == nil && n >= 1 && n <= 31 {
					d = n
				}
			}
			switch {
			case strings.Contains(s, "mid"):
				lo := future(dateOf(year, mo, 10, loc))
				return lo, lo.AddDate(0, 0, 11), true
			case strings.Contains(s, "end of") || strings.Contains(s, "late"):
				lo := future(dateOf(year, mo, 20, loc))
				return lo, dateOf(lo.Year(), mo+1, 1, loc), true
			case strings.Contains(s, "early") || strings.Contains(s, "beginning"):
				lo := future(dateOf(year, mo, 1, loc))
				return lo, lo.AddDate(0, 0, 10), true
			case d > 0:
				lo := future(dateOf(year, mo, d, loc))
				return lo, lo.AddDate(0, 0, 1), true
			}
			lo := future(dateOf(year, mo, 1, loc))
			return lo, lo.AddDate(0, 1, 0), true
		}
	}
	today := dateOf(said.Year(), said.Month(), said.Day(), loc)
	switch {
	case strings.Contains(s, "today") || strings.Contains(s, "tonight"):
		return today, today.AddDate(0, 0, 1), true
	case strings.Contains(s, "tomorrow"):
		return today.AddDate(0, 0, 1), today.AddDate(0, 0, 2), true
	case strings.Contains(s, "next week"):
		start := today.AddDate(0, 0, 7-int(today.Weekday())+1)
		return start, start.AddDate(0, 0, 7), true
	case strings.Contains(s, "this week"):
		start := today.AddDate(0, 0, -int(today.Weekday())+1)
		return start, start.AddDate(0, 0, 7), true
	case strings.Contains(s, "next month"):
		lo := dateOf(today.Year(), today.Month()+1, 1, loc)
		return lo, lo.AddDate(0, 1, 0), true
	case strings.Contains(s, "end of the month") || strings.Contains(s, "end of month"):
		lo := dateOf(today.Year(), today.Month(), 20, loc)
		return lo, dateOf(today.Year(), today.Month()+1, 1, loc), true
	case strings.Contains(s, "next quarter"):
		q := (int(today.Month())-1)/3 + 1
		lo := dateOf(today.Year(), time.Month(3*q+1), 1, loc)
		return lo, lo.AddDate(0, 3, 0), true
	}
	for _, w := range words {
		if wd, ok := weekdays[w]; ok {
			delta := (int(wd) - int(today.Weekday()) + 7) % 7
			if delta == 0 || strings.Contains(s, "next") {
				delta += 7
			}
			t := today.AddDate(0, 0, delta)
			return t, t.AddDate(0, 0, 1), true
		}
	}
	if m := reYear.FindStringSubmatch(s); m != nil && len(words) <= 2 {
		y, _ := strconv.Atoi(m[1])
		lo := dateOf(y, 1, 1, loc)
		return lo, lo.AddDate(1, 0, 0), true
	}
	return time.Time{}, time.Time{}, false
}

var numberWords = map[string]float64{
	"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16,
	"seventeen": 17, "eighteen": 18, "nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
	"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90, "a": 1, "half": 0.5,
}
var scaleWords = map[string]float64{"hundred": 100, "thousand": 1e3, "k": 1e3, "million": 1e6, "m": 1e6, "mm": 1e6, "billion": 1e9, "bn": 1e9, "b": 1e9}

var reNum = regexp.MustCompile(`(\d+(?:[.,]\d+)*)\s*(k|m|mm|bn|b|thousand|million|billion|hundred)?\b`)

// parseNumber reads "15%", "$40M", "152k", "forty million", "1,200".
func parseNumber(raw string) (float64, bool) {
	s := strings.ToLower(strings.ReplaceAll(raw, "$", ""))
	if m := reNum.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err == nil {
			if sc, ok := scaleWords[m[2]]; ok {
				n *= sc
			}
			return n, true
		}
	}
	total, cur, seen := 0.0, 0.0, false
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if n, ok := numberWords[w]; ok && !(w == "a" && seen) {
			cur += n
			seen = true
		} else if sc, ok := scaleWords[w]; ok && seen {
			if sc == 100 {
				cur *= sc
			} else {
				total += cur * sc
				cur = 0
			}
		}
	}
	if !seen {
		return 0, false
	}
	return total + cur, true
}

var unitSeconds = map[string]float64{
	"second": 1, "minute": 60, "hour": 3600, "day": 86400, "week": 7 * 86400, "month": 30 * 86400,
	"quarter": 91 * 86400, "year": 365 * 86400, "sprint": 14 * 86400,
}

// parseDuration reads "two years", "45 days", "three sprints".
func parseDuration(raw string) (float64, bool) {
	s := strings.ToLower(raw)
	n, ok := parseNumber(s)
	if !ok {
		n = 1
	}
	for u, sec := range unitSeconds {
		if strings.Contains(s, u) {
			return n * sec, true
		}
	}
	return 0, false
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

const monthRx = `(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// reDatePhrase finds the phrases parseDate reads, in free text: "October
// 1st", "1 November", "mid-December", "Q4 2026", "next week", "by Friday",
// "end of the year". A bare month needs a preposition before it, so "may"
// the verb and "march" the noun aren't dates.
var reDatePhrase = regexp.MustCompile(`(?i)\b(?:` +
	`\d{4}-\d{1,2}-\d{1,2}` +
	`|(?:early|mid|late|end of|beginning of)[- ]` + monthRx + `\b` +
	`|` + monthRx + `\.?\s+\d{1,2}(?:st|nd|rd|th)?\b(?:,?\s+20\d{2})?` +
	`|\d{1,2}(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthRx + `\b(?:\s+20\d{2})?` +
	`|(?:by|in|until|before|after|from|since|through|during)\s+` + monthRx + `\b(?:\.?\s+\d{1,2}(?:st|nd|rd|th)?\b)?(?:,?\s+20\d{2})?` +
	`|q[1-4](?:\s+20\d{2})?` +
	`|h[12](?:\s+20\d{2})?` +
	`|end of (?:the )?year|year[- ]end` +
	`|next week|this week|tomorrow|next month|end of (?:the )?month|next quarter` +
	`|(?:by|on|next|this)\s+(?:monday|tuesday|wednesday|thursday|friday)` +
	`)`)

// reAfter is a date relative to the one before it: "two weeks after
// that", "10 days later".
var reAfter = regexp.MustCompile(`(?i)\b(\d+|a|one|two|three|four|five|six|seven|eight|nine|ten)\s+(day|week|month)s?\s+(?:after|later)`)

// DatesIn returns the dates a text names, in order, relative to when it was
// said. Imported items and points carry no typed value; this lets a card
// say "due tomorrow" and lets linking compare plans by their dates.
func DatesIn(text string, said time.Time) []Value {
	type found struct {
		at int
		v  Value
	}
	var fs []found
	for _, ix := range reDatePhrase.FindAllStringIndex(text, -1) {
		m := text[ix[0]:ix[1]]
		raw := m
		if strings.Contains(strings.ToLower(m), "year") {
			raw = "late December"
		}
		if v := Normalize("date", raw, said); v.Normalized {
			v.Raw = m
			fs = append(fs, found{ix[0], v})
		}
	}
	for _, ix := range reAfter.FindAllStringSubmatchIndex(text, -1) {
		n, ok := parseNumber(text[ix[2]:ix[3]])
		if !ok {
			continue
		}
		var base *Value
		for k := range fs {
			if fs[k].at < ix[0] {
				base = &fs[k].v
			}
		}
		if base == nil {
			continue
		}
		lo := time.Unix(int64(base.Lo), 0).In(said.Location())
		switch strings.ToLower(text[ix[4]:ix[5]]) {
		case "day":
			lo = lo.AddDate(0, 0, int(n))
		case "week":
			lo = lo.AddDate(0, 0, 7*int(n))
		case "month":
			lo = lo.AddDate(0, int(n), 0)
		}
		fs = append(fs, found{ix[0], Value{Type: "date", Raw: text[ix[0]:ix[1]], Lo: float64(lo.Unix()), Hi: float64(lo.AddDate(0, 0, 1).Unix()), Normalized: true}})
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].at < fs[j].at })
	out := make([]Value, len(fs))
	for i, f := range fs {
		out[i] = f.v
	}
	return out
}

var reFigure = regexp.MustCompile(`(?i)[$€£]?\d+(?:[.,]\d+)*\s*(?:%|percent\b|k\b|m\b|mm\b|bn\b|million\b|thousand\b|billion\b)?`)

// ValueFromText gives a statement written without a typed value (imports,
// points) the value its kind is about: a commitment's or decision's date,
// a metric's first figure. Years alone ("2026") are not a metric's figure.
func ValueFromText(kind, text string, said time.Time) Value {
	switch kind {
	case "commitment", "decision", "outside_fact", "sensitivity_cue", "opinion":
		if ds := DatesIn(text, said); len(ds) > 0 {
			return ds[0]
		}
	case "metric":
		for _, m := range reFigure.FindAllString(text, -1) {
			m = strings.TrimSpace(m)
			if reYear.MatchString(m) && len(m) == 4 {
				continue
			}
			typ := "number"
			if strings.HasSuffix(m, "%") || strings.HasSuffix(strings.ToLower(m), "percent") {
				typ = "percent"
			} else if strings.ContainsAny(m[:1], "$€£") {
				typ = "money"
			}
			if v := Normalize(typ, m, said); v.Normalized {
				return v
			}
		}
	}
	return Normalize("none", "", said)
}
