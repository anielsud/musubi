package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// TestScopeFilterMatchesVisible checks the SQL scope filter against
// sensitivity.Visible over every combination of item level, item category,
// chunk escalation and audience, under ordinary and unusual scopes. Any
// disagreement is a leak (or an over-hide) in the SQL.
func TestScopeFilterMatchesVisible(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	owner, lee, dana := "owner@corp.example", "lee@corp.example", "dana@corp.example"
	corp := []string{"corp.example"}
	audiences := [][]string{{owner}, {owner, lee}, {owner, lee, dana}}
	cats := []sensitivity.Categories{0, sensitivity.HR, sensitivity.DealTerms, sensitivity.HR | sensitivity.DealTerms}
	escalations := []*sensitivity.Label{nil, {Level: sensitivity.Restricted, Categories: sensitivity.Compensation}, {Level: sensitivity.Confidential}}

	type row struct {
		id    string
		label sensitivity.Label
		aud   []string
	}
	var rows []row
	n := 0
	for lvl := sensitivity.Public; lvl <= sensitivity.Restricted; lvl++ {
		for _, c := range cats {
			for _, aud := range audiences {
				for _, esc := range escalations {
					n++
					id := fmt.Sprintf("t:%d", n)
					it := item.Item{ID: id, Source: "t", Ref: fmt.Sprint(n), Kind: item.KindEmail, Title: "x", OccurredAt: time.Unix(int64(n), 0),
						Segments: []item.Segment{{Text: "probe"}}}
					if _, err := s.UpsertItem(it); err != nil {
						t.Fatal(err)
					}
					if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "probe"}}); err != nil {
						t.Fatal(err)
					}
					il := sensitivity.Label{Level: lvl, Categories: c}
					cl := map[int]sensitivity.Label{}
					eff := il
					if esc != nil {
						cl[0] = *esc
						eff = sensitivity.Combine(il, *esc)
					}
					if err := s.ReplaceLabels(id, "v", "h", il, cl, aud); err != nil {
						t.Fatal(err)
					}
					rows = append(rows, row{id, eff, aud})
				}
			}
		}
	}
	// An item with chunks but no labels: must be invisible everywhere.
	unl := item.Item{ID: "t:unlabelled", Source: "t", Ref: "u", Kind: item.KindEmail, Segments: []item.Segment{{Text: "probe"}}}
	s.UpsertItem(unl)
	s.ReplaceChunks(unl.ID, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "probe"}})

	scopes := []sensitivity.Scope{
		sensitivity.Owner(), sensitivity.SharedScreen(), sensitivity.Cloud(),
		sensitivity.ReplyTo(corp, lee), sensitivity.ReplyTo(corp, lee, dana), sensitivity.ReplyTo(corp, "tom@customer.example"),
		sensitivity.ReplyTo(corp, lee, "tom@customer.example"),
		{Name: "confidential-no-categories", MaxLevel: sensitivity.Confidential},
		{Name: "restricted-dealterms-only", MaxLevel: sensitivity.Restricted, Allowed: sensitivity.DealTerms},
		{Name: "public-only", MaxLevel: sensitivity.Public},
		{Name: "audience-limited-level", MaxLevel: sensitivity.Confidential, Allowed: sensitivity.AllCategories, Audience: []string{lee}, Open: sensitivity.Internal},
		{Name: "audience-public-open", MaxLevel: sensitivity.Restricted, Allowed: sensitivity.AllCategories, Audience: []string{lee}, Open: sensitivity.Public},
	}
	for _, sc := range scopes {
		hits, err := s.Search(sc, SearchQuery{Text: "probe", K: 10000, Mode: ModeKeyword})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, h := range hits {
			got[h.ItemID] = true
		}
		if got["t:unlabelled"] {
			t.Errorf("%s: unlabelled item visible", sc.Name)
		}
		for _, r := range rows {
			want := sensitivity.Visible(r.label, r.aud, sc)
			if got[r.id] != want {
				t.Errorf("%s: %s %s/%s aud=%v: visible=%v want %v", sc.Name, r.id, r.label.Level, r.label.Categories, r.aud, got[r.id], want)
			}
		}
	}
}

// Statements carry their own label on top of their passage's (a point's
// personal detail, or the most sensitive passage it rests on); the claim
// reads must apply both.
func TestStatementFilterMatchesVisible(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	owner, lee := "owner@corp.example", "lee@corp.example"
	corp := []string{"corp.example"}
	own := []sensitivity.Label{{}, {Level: sensitivity.Restricted, Categories: sensitivity.Personal}, {Level: sensitivity.Confidential, Categories: sensitivity.DealTerms}}
	type row struct {
		claim int64
		label sensitivity.Label
		aud   []string
	}
	var rows []row
	var claims []int64
	n := 0
	for lvl := sensitivity.Public; lvl <= sensitivity.Confidential; lvl++ {
		for _, aud := range [][]string{{owner}, {owner, lee}} {
			for _, o := range own {
				n++
				id := fmt.Sprintf("t:%d", n)
				it := item.Item{ID: id, Source: "t", Ref: fmt.Sprint(n), Kind: item.KindMeeting, Title: "x", OccurredAt: time.Unix(int64(n), 0),
					Segments: []item.Segment{{Text: "probe"}}}
				if _, err := s.UpsertItem(it); err != nil {
					t.Fatal(err)
				}
				if err := s.ReplaceChunks(id, "v", "h", []Chunk{{Seq: 0, SegTo: 1, Text: "probe"}}); err != nil {
					t.Fatal(err)
				}
				il := sensitivity.Label{Level: lvl}
				if err := s.ReplaceLabels(id, "v", "h", il, nil, aud); err != nil {
					t.Fatal(err)
				}
				cs, _ := s.Chunks(id)
				st := Statement{ChunkID: cs[0].ID, Kind: "fact", Text: "probe", Holder: "Lee", SaidAt: time.Unix(int64(n), 0), Own: o,
					Status: "stated", Who: []string{"Lee"}, Evidence: []Evidence{{ChunkID: cs[0].ID, Line: 1, Quote: "probe"}}}
				if err := s.ReplaceStatements(id, "v", "h", []Statement{st}); err != nil {
					t.Fatal(err)
				}
				un, _ := s.UnlinkedStatements()
				c, err := s.NewClaim(un[0], []float32{1, 0})
				if err != nil {
					t.Fatal(err)
				}
				claims = append(claims, c)
				rows = append(rows, row{c, sensitivity.Combine(il, o), aud})
			}
		}
	}
	scopes := []sensitivity.Scope{sensitivity.Owner(), sensitivity.SharedScreen(), sensitivity.Cloud(),
		sensitivity.ReplyTo(corp, lee), sensitivity.ReplyTo(corp, "tom@customer.example")}
	for _, sc := range scopes {
		ev, err := s.Evidence(sc, claims)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			want := sensitivity.Visible(r.label, r.aud, sc)
			if got := ev[r.claim] != nil; got != want {
				t.Errorf("%s: %s/%s aud=%v: visible=%v want %v", sc.Name, r.label.Level, r.label.Categories, r.aud, got, want)
			}
			if e := ev[r.claim]; e != nil && (len(e.Statements[0].Who) != 1 || len(e.Statements[0].Evidence) != 1) {
				t.Errorf("point fields not read back: %+v", e.Statements[0])
			}
		}
	}
	// Owner sees personal points; a reply to the person never does.
	if !sensitivity.Visible(sensitivity.Label{Level: sensitivity.Restricted, Categories: sensitivity.Personal}, []string{owner, lee}, sensitivity.Owner()) ||
		sensitivity.Visible(sensitivity.Label{Level: sensitivity.Restricted, Categories: sensitivity.Personal}, []string{owner, lee}, sensitivity.ReplyTo(corp, lee)) {
		t.Error("personal visibility rule changed")
	}
}

func TestKnowledgeSignature(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir + "/k.db")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s0, _ := a.KnowledgeSignature()
	if s1, _ := a.KnowledgeSignature(); s1 != s0 {
		t.Fatal("signature moved without a write")
	}
	// Our own write.
	if err := a.SetGlobalStamp("x", "1"); err != nil {
		t.Fatal(err)
	}
	s1, _ := a.KnowledgeSignature()
	if s1 == s0 {
		t.Fatal("own write not seen")
	}
	// Another process's write (another connection to the same file).
	b, err := Open(dir + "/k.db")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.SetGlobalStamp("x", "2"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := a.KnowledgeSignature(); s2 == s1 {
		t.Fatal("another connection's write not seen")
	}
}
