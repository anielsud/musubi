package knowledge

import (
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/store"
)

var t0 = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func st(id int64, holder, kind, cert, src string, at time.Time) store.Statement {
	c := map[string]float64{"high": 1, "medium": 0.7, "low": 0.4}[cert]
	return store.Statement{ID: id, Holder: holder, Kind: kind, Certainty: c, ItemKind: src, SaidAt: at}
}

func ev(kind string, sts ...store.Statement) *store.ClaimEvidence {
	return &store.ClaimEvidence{ClaimID: 1, Kind: kind, Statements: sts, Related: map[int64][]store.Statement{}}
}

func TestIndependentHoldersOutweighRepetition(t *testing.T) {
	now := t0.Add(24 * time.Hour)
	one := Compute(ev("fact", st(1, "a", "fact", "high", "meeting", t0), st(2, "a", "fact", "high", "meeting", t0), st(3, "a", "fact", "high", "meeting", t0)), now)
	two := Compute(ev("fact", st(1, "a", "fact", "high", "meeting", t0), st(2, "b", "fact", "high", "meeting", t0)), now)
	if two.Support <= one.Support {
		t.Errorf("two holders %.2f should beat one holder thrice %.2f", two.Support, one.Support)
	}
	if two.Status != "established" {
		t.Errorf("two independent holders: %s (%s)", two.Status, two.Why)
	}
	if one.Status == "established" {
		t.Errorf("one holder repeating shouldn't be established: %.2f", one.Score)
	}
}

func TestWrittenEstablishes(t *testing.T) {
	b := Compute(ev("fact", st(1, "a", "fact", "high", "email", t0)), t0.Add(time.Hour))
	if b.Status != "established" {
		t.Errorf("a high-certainty written fact: %s %.2f", b.Status, b.Score)
	}
}

func TestOpinionsAreSpeculative(t *testing.T) {
	b := Compute(ev("opinion", st(1, "a", "opinion", "high", "meeting", t0), st(2, "b", "hypothesis", "medium", "meeting", t0)), t0.Add(time.Hour))
	if b.Status != "speculative" {
		t.Errorf("opinions only: %s", b.Status)
	}
}

func TestDecay(t *testing.T) {
	e := ev("hypothesis", st(1, "a", "hypothesis", "high", "meeting", t0))
	fresh := Compute(e, t0.Add(time.Hour))
	old := Compute(e, t0.Add(100*24*time.Hour))
	if old.Support >= fresh.Support || old.Status != "stale" {
		t.Errorf("hypothesis after 100 days: %.2f (%s), fresh %.2f", old.Support, old.Status, fresh.Support)
	}
	d := Compute(ev("decision", st(1, "a", "decision", "high", "meeting", t0)), t0.Add(400*24*time.Hour))
	if d.Status == "stale" {
		t.Error("decisions don't go stale; they get superseded")
	}
}

func TestContradictionAndSupersession(t *testing.T) {
	// A hypothesis later contradicted by a fact.
	e := ev("hypothesis", st(1, "lee", "hypothesis", "medium", "chat", t0))
	e.Relations = []store.Relation{{From: 2, To: 1, Rel: "contradicts", StatementID: 5}}
	e.Related[2] = []store.Statement{st(5, "priya", "fact", "high", "meeting", t0.Add(48*time.Hour))}
	b := Compute(e, t0.Add(72*time.Hour))
	if b.Status != "disproven" {
		t.Errorf("contradicted by a later fact: %s (support %.2f against %.2f)", b.Status, b.Support, b.Against)
	}
	// The contradiction isn't visible in a narrower scope: no effect.
	e.Related[2] = nil
	if b := Compute(e, t0.Add(72*time.Hour)); b.Against != 0 {
		t.Errorf("invisible contradiction counted: %.2f", b.Against)
	}
	// A decision superseded by a later one.
	d := ev("decision", st(1, "jordan", "decision", "high", "meeting", t0))
	d.Relations = []store.Relation{{From: 3, To: 1, Rel: "supersedes", StatementID: 7}}
	d.Related[3] = []store.Statement{st(7, "priya", "fact", "high", "meeting", t0.Add(7*24*time.Hour))}
	if b := Compute(d, t0.Add(8*24*time.Hour)); b.Status != "superseded" {
		t.Errorf("superseded decision: %s", b.Status)
	}
}

func TestQuestionAnswered(t *testing.T) {
	q := ev("question", st(1, "tom", "question", "high", "email", t0))
	if b := Compute(q, t0.Add(time.Hour)); b.Status != "open" {
		t.Errorf("open question: %s", b.Status)
	}
	q.Relations = []store.Relation{{From: 2, To: 1, Rel: "answers", StatementID: 9}}
	q.Related[2] = []store.Statement{st(9, "lee", "fact", "high", "meeting", t0.Add(24*time.Hour))}
	if b := Compute(q, t0.Add(48*time.Hour)); b.Status != "answered" {
		t.Errorf("answered question: %s", b.Status)
	}
}

func TestJudgementWins(t *testing.T) {
	e := ev("fact", st(1, "a", "fact", "high", "email", t0))
	e.Judgments = []store.Judgment{{StatementID: 1, Verdict: "false"}}
	if b := Compute(e, t0.Add(time.Hour)); b.Status != "disproven" {
		t.Errorf("judged false: %s", b.Status)
	}
}

func TestSameFigures(t *testing.T) {
	if sameFigures("The batching fix will ship on September 29", "The batching fix will be shipped on September 22.") {
		t.Error("different dates read as the same")
	}
	if !sameFigures("Discount is 15% for two years", "A 15% discount over two years") {
		t.Error("same figures read as different")
	}
}
