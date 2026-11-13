package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/eval"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// shadowCmd groups the commands that check the recorder against Tomoe
// while both record the same meetings.
var shadowCmd = &cobra.Command{
	Use:   "shadow",
	Short: "Compare the recorder's recordings with Tomoe's",
}

var shadowCompareCmd = &cobra.Command{
	Use:   "compare",
	Short: "Pair each recorder session with Tomoe's for the same meeting and compare them",
	Long: `Pairs each session in the recorder's folder with the Tomoe session that
overlaps it most in time and prints, per pair: how far apart they started
and stopped, both durations and line counts, the word error rate of the
recorder's text against Tomoe's, and how often the two give an aligned
word the same speaker (by name; an unnamed "Person N" by the unnamed
Tomoe speaker it most often lines up with). Recordings only one side has are
listed after.

Reads both folders and writes nothing. The output is for the terminal:
don't commit it anywhere.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		data, _ := cmd.Flags().GetString("data")
		sinceFlag, _ := cmd.Flags().GetString("since")
		var since time.Time
		if sinceFlag != "" {
			t, err := time.ParseInLocation("2006-01-02", sinceFlag, time.Local)
			if err != nil {
				return fmt.Errorf("--since: want a date like 2026-11-11: %w", err)
			}
			since = t
		}
		dir, err := resolveDataDir(data)
		if err != nil {
			return err
		}
		ours, err := sessionsSince(filepath.Join(dir, "sessions"), since)
		if err != nil {
			return err
		}
		// Without --since, Tomoe's sessions from before the recorder's
		// first one aren't news: start there, less a long meeting's worth
		// so one that began earlier still pairs.
		from := since
		if from.IsZero() && len(ours) > 0 {
			from = ours[len(ours)-1].CreatedAt.Add(-shadowLookBack) // List is newest first
		}
		theirs, err := sessionsSince(filepath.Join(config.TomoeDataDir(), "sessions"), from)
		if err != nil {
			return err
		}
		rep := compareShadow(ours, theirs)
		if since.IsZero() && len(ours) > 0 {
			rep.onlyTheirs = endingAfter(rep.onlyTheirs, ours[len(ours)-1].CreatedAt)
		}
		printShadowCompare(os.Stdout, rep)
		return nil
	},
}

func init() {
	shadowCompareCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder")
	shadowCompareCmd.Flags().String("since", "", "only sessions started on or after this date (YYYY-MM-DD)")
	shadowCmd.AddCommand(shadowCompareCmd)
	rootCmd.AddCommand(shadowCmd)
}

// shadowLookBack is how long before the recorder's first session a Tomoe
// session may have started and still be compared.
const shadowLookBack = 12 * time.Hour

// endingAfter is the sessions in ss still recording at t or later.
func endingAfter(ss []*session.Session, t time.Time) []*session.Session {
	var out []*session.Session
	for _, s := range ss {
		if _, end := sessionSpan(s); !end.Before(t) {
			out = append(out, s)
		}
	}
	return out
}

// sessionsSince loads the sessions under dir that started at or after
// since (all of them for a zero since).
func sessionsSince(dir string, since time.Time) ([]*session.Session, error) {
	all, err := session.NewStore(dir).List()
	if err != nil {
		return nil, err
	}
	var out []*session.Session
	for _, s := range all {
		if !s.CreatedAt.Before(since) {
			out = append(out, s)
		}
	}
	return out, nil
}

// shadowPair is one meeting both recorded.
type shadowPair struct {
	ours, theirs *session.Session
	overlap      time.Duration
}

// shadowReport is compareShadow's result: the pairs in time order, and
// the sessions only one side has.
type shadowReport struct {
	pairs      []shadowPair
	onlyOurs   []*session.Session
	onlyTheirs []*session.Session
}

// sessionSpan is when a session recorded. One still open, or saved
// without an end, ends after its duration.
func sessionSpan(s *session.Session) (start, end time.Time) {
	start, end = s.CreatedAt, s.EndedAt
	if end.IsZero() {
		end = start.Add(time.Duration(s.Duration * float64(time.Second)))
	}
	return start, end
}

// overlapOf is how long a and b were both recording.
func overlapOf(a, b *session.Session) time.Duration {
	as, ae := sessionSpan(a)
	bs, be := sessionSpan(b)
	from, to := as, ae
	if bs.After(from) {
		from = bs
	}
	if be.Before(to) {
		to = be
	}
	return max(to.Sub(from), 0)
}

// compareShadow pairs ours with theirs one to one, the largest overlaps
// first, so two short recordings during one long one don't both claim
// it. Sessions that overlap nothing on the other side stay unpaired.
func compareShadow(ours, theirs []*session.Session) shadowReport {
	type cand struct {
		i, j int
		d    time.Duration
	}
	var cands []cand
	for i, o := range ours {
		for j, t := range theirs {
			if d := overlapOf(o, t); d > 0 {
				cands = append(cands, cand{i, j, d})
			}
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].d > cands[b].d })
	oursTaken, theirsTaken := map[int]bool{}, map[int]bool{}
	var rep shadowReport
	for _, c := range cands {
		if oursTaken[c.i] || theirsTaken[c.j] {
			continue
		}
		oursTaken[c.i], theirsTaken[c.j] = true, true
		rep.pairs = append(rep.pairs, shadowPair{ours: ours[c.i], theirs: theirs[c.j], overlap: c.d})
	}
	sort.Slice(rep.pairs, func(a, b int) bool { return rep.pairs[a].ours.CreatedAt.Before(rep.pairs[b].ours.CreatedAt) })
	for i, o := range ours {
		if !oursTaken[i] {
			rep.onlyOurs = append(rep.onlyOurs, o)
		}
	}
	for j, t := range theirs {
		if !theirsTaken[j] {
			rep.onlyTheirs = append(rep.onlyTheirs, t)
		}
	}
	return rep
}

// spokenWords is a session's words in spoken order, normalized for
// scoring (eval.Words), each with the speaker of its line.
func spokenWords(s *session.Session) (words, speakers []string) {
	segs := append([]session.Segment(nil), s.Segments...)
	sort.SliceStable(segs, func(a, b int) bool { return segs[a].StartTime < segs[b].StartTime })
	for _, seg := range segs {
		for _, w := range eval.Words(seg.Text) {
			words = append(words, w)
			speakers = append(speakers, seg.Speaker)
		}
	}
	return words, speakers
}

// textScore is how the recorder's text compares with Tomoe's: its word
// error rate with Tomoe's as the reference, and, over the words both
// transcripts have at the same place in the alignment, how many carry
// the same speaker (see sameSpeaker).
type textScore struct {
	wer           float64
	matched, same int
}

func scoreText(ours, theirs *session.Session) textScore {
	hyp, hypSpk := spokenWords(ours)
	ref, refSpk := spokenWords(theirs)
	score := textScore{wer: eval.Align(ref, hyp).Rate()}
	// Words outside DiffText's hunks are equal and aligned one to one.
	var pairs [][2]string // Tomoe's speaker, ours, per aligned word
	i, j := 0, 0
	collect := func(ri, hj int) {
		for ; i < ri && j < hj; i, j = i+1, j+1 {
			pairs = append(pairs, [2]string{speakerName(refSpk[i]), speakerName(hypSpk[j])})
		}
	}
	for _, h := range eval.DiffText(ref, hyp) {
		collect(h.RefFrom, h.HypFrom)
		i, j = h.RefTo, h.HypTo
	}
	collect(len(ref), len(hyp))
	unnamed := unnamedMatches(pairs)
	for _, p := range pairs {
		score.matched++
		if p[0] == p[1] || unnamed[p[1]] == p[0] {
			score.same++
		}
	}
	return score
}

// speakerName is the person a speaker label names: "Person 3 (Ann)" is
// Ann. Each recorder numbers its own speakers, so comparing whole labels
// counted the same named person as a disagreement.
func speakerName(label string) string {
	if m := personLabel.FindStringSubmatch(label); m != nil && m[2] != "" {
		return m[2]
	}
	return label
}

var personLabel = regexp.MustCompile(`^Person (\d+)(?: \((.*)\))?$`)

// unnamedMatches pairs each of our unnamed speakers ("Person N", no name)
// with the unnamed Tomoe speaker its words most often align with: the
// numbers are each recorder's own, so only that correspondence can be
// compared. An unnamed speaker against a named one stays a disagreement.
// pairs hold speakerName results, so "Person N" there is unnamed.
func unnamedMatches(pairs [][2]string) map[string]string {
	counts := map[string]map[string]int{}
	for _, p := range pairs {
		if !personLabel.MatchString(p[0]) || !personLabel.MatchString(p[1]) {
			continue
		}
		if counts[p[1]] == nil {
			counts[p[1]] = map[string]int{}
		}
		counts[p[1]][p[0]]++
	}
	best := map[string]string{}
	for ours, theirs := range counts {
		n := 0
		for t, c := range theirs {
			if c > n || (c == n && t < best[ours]) {
				best[ours], n = t, c
			}
		}
	}
	return best
}

func printShadowCompare(w io.Writer, rep shadowReport) {
	if len(rep.pairs) == 0 {
		fmt.Fprintln(w, "No meeting was recorded by both.")
	}
	for _, p := range rep.pairs {
		ourStart, ourEnd := sessionSpan(p.ours)
		tomoeStart, tomoeEnd := sessionSpan(p.theirs)
		sc := scoreText(p.ours, p.theirs)
		agree := "n/a"
		if sc.matched > 0 {
			agree = fmt.Sprintf("%.1f%% of %d words", 100*float64(sc.same)/float64(sc.matched), sc.matched)
		}
		fmt.Fprintf(w, "%s  recorder %s / Tomoe %s\n", ourStart.Local().Format("2006-01-02 15:04"), p.ours.ID, p.theirs.ID)
		fmt.Fprintf(w, "  start %+.1fs, stop %+.1fs (recorder minus Tomoe)\n", ourStart.Sub(tomoeStart).Seconds(), ourEnd.Sub(tomoeEnd).Seconds())
		fmt.Fprintf(w, "  duration %.0fs / %.0fs, lines %d / %d\n", ourEnd.Sub(ourStart).Seconds(), tomoeEnd.Sub(tomoeStart).Seconds(), len(p.ours.Segments), len(p.theirs.Segments))
		fmt.Fprintf(w, "  WER against Tomoe %.1f%%, same speaker %s\n", 100*sc.wer, agree)
	}
	for _, s := range rep.onlyOurs {
		fmt.Fprintf(w, "only the recorder: %s %s\n", s.CreatedAt.Local().Format("2006-01-02 15:04"), s.ID)
	}
	for _, s := range rep.onlyTheirs {
		fmt.Fprintf(w, "only Tomoe: %s %s\n", s.CreatedAt.Local().Format("2006-01-02 15:04"), s.ID)
	}
}
