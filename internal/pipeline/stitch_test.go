package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// A Teams transcript of a whole meeting: one line a minute, 10:00–11:00,
// each with its own words.
func teamsCopy(id string, at time.Time) item.Item {
	t := item.Item{ID: id, Source: "teams", Ref: "teams-" + id, Kind: item.KindMeeting, OccurredAt: at, EndedAt: at.Add(time.Hour)}
	for m := 0; m < 60; m++ {
		t.Segments = append(t.Segments, item.Segment{Speaker: "Robin Vale", Start: float64(m * 60), End: float64(m*60 + 50), Text: fmt.Sprintf("minute%02d alpha%02d bravo%02d charlie", m, m, m)})
	}
	return t
}

// A recording of from..to minutes of that meeting, hearing the same words.
func recording(id string, at time.Time, from, to int) item.Item {
	r := item.Item{ID: id, Source: "tomoe", Ref: id, Kind: item.KindMeeting, OccurredAt: at.Add(time.Duration(from) * time.Minute), EndedAt: at.Add(time.Duration(to) * time.Minute)}
	for m := from; m < to; m++ {
		r.Segments = append(r.Segments, item.Segment{Speaker: "You", Start: float64((m - from) * 60), End: float64((m-from)*60 + 50), Text: fmt.Sprintf("minute%02d alpha%02d bravo%02d charlie", m, m, m)})
	}
	return r
}

func TestStitch(t *testing.T) {
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	links := map[string]string{"late": "ev", "teams": "ev"}

	// Joined 45 minutes late: the first 45 minutes come from Teams.
	items := []item.Item{recording("late", at, 45, 60), teamsCopy("teams", at)}
	fills, from := stitchFills(items, links)
	if len(fills["late"]) != 45 || from["late"] != "teams" {
		t.Fatalf("late join: %d lines from %q", len(fills["late"]), from["late"])
	}
	s := stitched(items[0], fills["late"])
	if !s.OccurredAt.Equal(at) || len(s.Segments) != 60 || s.Segments[0].From != "teams" || s.Segments[59].From != "" {
		t.Fatalf("stitched: start %v, %d lines, first from %q, last from %q", s.OccurredAt, len(s.Segments), s.Segments[0].From, s.Segments[59].From)
	}
	if got := s.Segments[45]; got.Speaker != "You" || got.Start != 45*60 {
		t.Errorf("own first line shifted to %v (%s)", got.Start, got.Speaker)
	}
	if len(items[0].Segments) != 15 || !items[0].OccurredAt.Equal(at.Add(45*time.Minute)) {
		t.Error("stitching changed the stored recording")
	}

	// Recorded in two parts with a gap, and left early: the gap goes to
	// the second part, the end to the last. A line starting as a part
	// stops counts as heard (stitchSlack), so 9 between and 9 after.
	links = map[string]string{"a": "ev", "b": "ev", "teams": "ev"}
	items = []item.Item{recording("a", at, 0, 20), recording("b", at, 30, 50), teamsCopy("teams", at)}
	fills, _ = stitchFills(items, links)
	if len(fills["a"]) != 0 || len(fills["b"]) != 18 {
		t.Errorf("split: a %d, b %d lines (want 0, 18)", len(fills["a"]), len(fills["b"]))
	}

	// Another conversation in the same slot: not stitched.
	other := recording("x", at, 30, 60)
	for i := range other.Segments {
		other.Segments[i].Text = fmt.Sprintf("unrelated%02d words%02d entirely", i, i)
	}
	fills, _ = stitchFills([]item.Item{other, teamsCopy("teams", at)}, map[string]string{"x": "ev", "teams": "ev"})
	if len(fills) != 0 {
		t.Errorf("different conversation stitched: %d", len(fills["x"]))
	}

	// The stitched-in copy is the recording's duplicate, and so is
	// whatever stood in for it.
	dups := withStitches(map[string]string{"late": "teams", "bluedot": "teams"}, map[string]string{"teams": "late"})
	if dups["teams"] != "late" || dups["late"] != "" || dups["bluedot"] != "late" {
		t.Errorf("dups: %v", dups)
	}
}

// A recording waiting for its meeting's Teams transcript is extracted
// (and summarised) at once, but not threaded until the transcript can't
// change it. When the transcript comes, the recording is stitched and
// extracted again; once threaded, a transcript arriving late changes
// nothing.
func TestWaitingNotThreaded(t *testing.T) {
	for _, threadedFirst := range []bool{false, true} {
		r, st := build(t)
		kc := &r.Cfg.Knowledge
		kc.Extract, kc.Gate, kc.Grouping, kc.Stitch, kc.StitchWaitHours = "statements", 0, "threads", true, 2
		r.Cfg.M365.Enabled, r.Cfg.M365.Transcripts = true, true
		at := time.Now().Add(-time.Hour).Truncate(time.Second)
		ev := item.Item{ID: "m365-calendar:ev", Source: "m365-calendar", Ref: "ev", Kind: item.KindEvent, Title: "Weekly", OccurredAt: at, EndedAt: at.Add(30 * time.Minute),
			Native: map[string]string{"joinUrl": "https://teams.example/join"}}
		rec := recording("rec", at, 20, 30) // joined 20 minutes late
		rec.Title = "Weekly"
		for _, it := range []item.Item{ev, rec} {
			if _, err := st.UpsertItem(it); err != nil {
				t.Fatal(err)
			}
		}
		run(t, r)
		knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m1"}})
		if s, _ := st.StageStamp("extract", rec.ID); s == "" {
			t.Fatal("a recording waiting for its transcript wasn't extracted")
		}
		if done, _ := st.ThreadedItems(); done[rec.ID] != "" {
			t.Fatal("a recording waiting for its transcript was threaded")
		}
		if threadedFirst {
			st.MarkThreaded(rec.ID, "s")
		}

		tc := teamsCopy("teams", at)
		tc.Title = "Weekly"
		if _, err := st.UpsertItem(tc); err != nil {
			t.Fatal(err)
		}
		run(t, r)
		chunks, _ := st.Chunks(rec.ID)
		heard := false
		for _, c := range chunks {
			heard = heard || strings.Contains(c.Text, "minute00")
		}
		if heard == threadedFirst {
			t.Errorf("threaded first %v: stitched %v", threadedFirst, heard)
		}
		knowledgeRun(t, r, KnowledgeOptions{LLM: emptyLLM{"m1"}})
		if s, _ := st.StageStamp("extract", rec.ID); s == "" {
			t.Errorf("threaded first %v: not extracted again after stitching", threadedFirst)
		}
	}
}
