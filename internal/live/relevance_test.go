package live

import (
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
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
