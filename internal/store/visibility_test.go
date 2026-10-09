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
