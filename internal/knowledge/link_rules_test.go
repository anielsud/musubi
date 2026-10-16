package knowledge

import (
	"testing"

	"github.com/anielsud/musubi/internal/store"
)

func TestStrictRule(t *testing.T) {
	claim := func(kind, item string, who ...string) *liveClaim {
		return &liveClaim{ClaimRow: store.ClaimRow{Kind: kind, Items: map[string]bool{item: true}, Who: who}}
	}
	cases := []struct {
		name, rel string
		st        store.Statement
		c         *liveClaim
		want      string
	}{
		{"same meeting, no correction: adds detail", "supersedes",
			store.Statement{Kind: "fact", ItemID: "m1", Text: "The remaining buckets are sales, then ops."}, claim("fact", "m1"), "refines"},
		{"same meeting, spoken correction", "supersedes",
			store.Statement{Kind: "metric", ItemID: "m1", Text: "Correcting the earlier update: churn was 12.9."}, claim("metric", "m1"), "supersedes"},
		{"later meeting replaces", "supersedes",
			store.Statement{Kind: "metric", ItemID: "m2", Text: "Churn is 12.9."}, claim("metric", "m1"), "supersedes"},
		{"promise to find out leaves the question open", "answers",
			store.Statement{Kind: "commitment", ItemID: "m1", Text: "Dana will get the exact count from finance."}, claim("question", "m1"), "pending"},
		{"different owners' commitments are unrelated", "supersedes",
			store.Statement{Kind: "commitment", ItemID: "m2", Who: []string{"Dana Ruiz"}, Text: "Dana will send the list."}, claim("commitment", "m1", "Lee Chen"), ""},
		{"same owner may refine", "refines",
			store.Statement{Kind: "commitment", ItemID: "m2", Who: []string{"Lee Chen"}, Text: "Lee will send it Friday."}, claim("commitment", "m1", "Lee Chen"), "refines"},
	}
	for _, c := range cases {
		if got := strictRule(c.rel, c.st, c.c); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
