package live

import (
	"context"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/provider"
)

// What fits takes the places; what is shown keeps its place against a
// newcomer that fits only a little better, and gives it up to one that
// fits clearly better or when it stops fitting.
func TestChoosePlaces(t *testing.T) {
	l := &Live{eng: &engine.Engine{Cfg: config.DefaultConfig()}}
	l.stepped.Store(true)
	set := func(key string, score float64) {
		if l.places == nil {
			l.places = map[string]*place{}
		}
		p := l.places[key]
		if p == nil {
			p = &place{key: key}
			l.places[key] = p
		}
		p.score, p.offered = score, l.stepClock()
	}
	openKeys := func() map[string]bool {
		out := map[string]bool{}
		for k, p := range l.places {
			if p.open {
				out[k] = true
			}
		}
		return out
	}
	set("a", 0.9)
	set("b", 0.8)
	set("c", 0.7)
	set("d", 0.6)
	set("low", 0.3) // below the bar: never shown
	l.choose()
	if o := openKeys(); len(o) != 4 || o["low"] {
		t.Fatalf("first choice %v", o)
	}
	set("e", 0.7) // a little better than d: d keeps its place
	l.choose()
	if o := openKeys(); !o["d"] || o["e"] {
		t.Errorf("a slightly better newcomer displaced: %v", o)
	}
	set("f", 0.95) // clearly better than the weakest
	l.choose()
	if o := openKeys(); !o["f"] || o["d"] {
		t.Errorf("a clearly better newcomer didn't displace: %v", o)
	}
	set("a", 0.2) // stopped fitting: its place goes to the next best
	l.choose()
	if o := openKeys(); o["a"] || !o["e"] {
		t.Errorf("after a stopped fitting: %v", o)
	}
	// Raised long ago and never shown: dropped; once shown: kept, closed.
	l.setStepClock(1000)
	l.choose()
	if _, ok := l.places["low"]; ok {
		t.Error("an old item never shown is kept")
	}
	if p := l.places["a"]; p == nil || p.open || !p.wasOpen {
		t.Errorf("an old item once shown: %+v", p)
	}
}

type fitsClassifier float64

func (f fitsClassifier) Classify(context.Context, any, map[string]provider.Question) (map[string]provider.Answer, error) {
	return map[string]provider.Answer{"helps": {Noul: float64(f)}}, nil
}
func (fitsClassifier) Name() string { return "fits" }

// With the picker on, a shown lookup is still judged again, and gives up its
// place when it stops fitting (it used to keep it for the whole meeting).
func TestRejudgeWithPicker(t *testing.T) {
	var sent []Brief
	l := &Live{eng: &engine.Engine{Cfg: config.DefaultConfig()}, rel: fitsClassifier(0.1),
		emit: func(ev string, v any) {
			if b, ok := v.(Brief); ok && ev == "live:brief" {
				sent = append(sent, b)
			}
		}}
	l.stepped.Store(true)
	l.setStepClock(0)
	l.offerScored(briefKey("q3 numbers"), "LOOKUP: q3 numbers", &Brief{Query: "q3 numbers"}, 0.9)
	l.choose()
	if open, _, _ := l.placeOf(briefKey("q3 numbers")); !open {
		t.Fatal("not shown")
	}
	l.setStepClock(rescoreAfter + 1)
	l.meeting = NewMeeting(Session{})
	l.meeting.Apply(Segment{ID: "1", Text: "something else entirely", Start: rescoreAfter, End: rescoreAfter + 1})
	l.rejudge(1, true)
	if open, wasOpen, _ := l.placeOf(briefKey("q3 numbers")); open || !wasOpen {
		t.Fatalf("open %v wasOpen %v after it stopped fitting", open, wasOpen)
	}
	if len(sent) != 1 || sent[0].Open {
		t.Fatalf("closing not sent: %+v", sent)
	}
}
