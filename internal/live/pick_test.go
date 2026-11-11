package live

import (
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/engine"
	"github.com/anielsud/musubi/internal/knowledge"
)

// Votes older than the span stop counting; the picked cards open, best
// first, with briefs written under the same scope only.
func TestPickedCards(t *testing.T) {
	l := &Live{eng: &engine.Engine{Cfg: config.DefaultConfig()}, emit: func(string, any) {}, scope: "owner", pk: newPickState()}
	l.stepped.Store(true)
	l.setStepClock(0)
	l.candidate(1, 1, "old")
	l.setStepClock(pickSpan + 10)
	l.candidate(2, 1, "new")
	if len(l.pk.hits) != 1 || l.pk.hits[0].topic != 2 {
		t.Fatalf("hits %+v", l.pk.hits)
	}

	l.pk.picked = map[int64]float64{2: 0.5, 3: 0.9}
	l.pk.briefs[3] = &cardBrief{Headline: "where it stands", Raise: "ask about it", scope: "owner"}
	l.pk.briefs[2] = &cardBrief{Headline: "private", scope: "shared"}
	card := func(id int64) TopicCard {
		return TopicCard{Card: &knowledge.Card{TopicID: id, Anchor: knowledge.Anchor{Topic: id}}}
	}
	cards := []TopicCard{card(1), card(2), card(3)}
	l.pickedCards(cards, "")
	if cards[0].TopicID != 3 || cards[1].TopicID != 2 || !cards[0].Active || !cards[1].Active || cards[2].Active {
		t.Fatalf("order/open: %d %v, %d %v, %d %v", cards[0].TopicID, cards[0].Active, cards[1].TopicID, cards[1].Active, cards[2].TopicID, cards[2].Active)
	}
	if cards[0].Raise != "ask about it" || cards[1].Headline != "" {
		t.Fatalf("briefs: %q, %q", cards[0].Raise, cards[1].Headline)
	}
}

// A brief's raise lines come as a list (best first, at most three) or, from
// a model that ignores the format, one string.
func TestParseBrief(t *testing.T) {
	b, err := parseBrief("ok: {\"headline\": \" Launch slipped to May \", \"raise\": [\"Who owns the fix?\", \"\", \"Is the budget agreed?\", \"Any risk to Q3?\", \"A fourth\"]}")
	if err != nil || b.Headline != "Launch slipped to May" || b.Raise != "Who owns the fix?" || len(b.More) != 2 || b.More[1] != "Any risk to Q3?" {
		t.Fatalf("list: %+v %v", b, err)
	}
	b, err = parseBrief(`{"headline": "h", "raise": "One thing"}`)
	if err != nil || b.Raise != "One thing" || len(b.More) != 0 {
		t.Fatalf("string: %+v %v", b, err)
	}
}

// A brief that finds the notes don't cover the conversation takes its card
// down; one that doesn't say fits.
func TestBriefFits(t *testing.T) {
	b, err := parseBrief(`{"fits": false, "headline": "", "raise": []}`)
	if err != nil || !b.Off {
		t.Fatalf("unfit: %+v %v", b, err)
	}
	if b, _ = parseBrief(`{"headline": "h", "raise": ["r"]}`); b.Off {
		t.Fatalf("no fits field: %+v", b)
	}
	l := &Live{eng: &engine.Engine{Cfg: config.DefaultConfig()}, emit: func(string, any) {}, pk: newPickState()}
	a, other := knowledge.Anchor{Topic: 7}, knowledge.Anchor{Topic: 8}
	l.topics = []shownTopic{{id: a}, {id: other}}
	l.dropped = []shownTopic{{id: a}}
	l.pk.picked = map[int64]float64{7: 0.5, 8: 0.6}
	l.takeDownLocked(a)
	if len(l.topics) != 1 || l.topics[0].id != other || len(l.dropped) != 0 || len(l.pk.picked) != 1 {
		t.Fatalf("taken down: %+v %+v %+v", l.topics, l.dropped, l.pk.picked)
	}
}

// A brief reads from a little before its subject was first put forward,
// within the span; with full_window, the whole span.
func TestBriefSpan(t *testing.T) {
	l := &Live{eng: &engine.Engine{Cfg: config.DefaultConfig()}, pk: newPickState()}
	l.pk.firstAt[1], l.pk.firstAt[2] = 1000, 500
	if s := l.briefSpanLocked(1, 1030); s != 30+briefLead {
		t.Fatalf("recent subject: %v", s)
	}
	if s := l.briefSpanLocked(2, 1030); s != pickSpan {
		t.Fatalf("old subject: %v", s)
	}
	if s := l.briefSpanLocked(3, 1030); s != pickSpan {
		t.Fatalf("unknown: %v", s)
	}
	l.eng.Cfg.Live.Pick.FullWindow = true
	if s := l.briefSpanLocked(1, 1030); s != pickSpan {
		t.Fatalf("full window: %v", s)
	}
}

// The brief's conversation in two parts: before the topic came up, and
// since.
func TestRecentSplit(t *testing.T) {
	m := NewMeeting(Session{})
	m.Apply(Segment{ID: "1", Speaker: "You", Text: "the last account", Start: 0, End: 10})
	m.Apply(Segment{ID: "2", Speaker: "Person 2 (Ana Ruiz)", Text: "and finally, the next one", Start: 40, End: 50})
	b, a := m.RecentSplit(pickSpan, 20)
	if b != "You: the last account\n" || a != "Ana Ruiz: and finally, the next one\n" {
		t.Fatalf("before %q, since %q", b, a)
	}
}
