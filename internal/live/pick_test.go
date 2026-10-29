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
