package store

import (
	"testing"

	"github.com/anielsud/musubi/internal/sensitivity"
)

// TestReadCacheScopes: what the cache keeps under one scope is never
// served under another (the confidential statement stays off the shared
// screen after the owner read it), and its answers are the store's.
func TestReadCacheScopes(t *testing.T) {
	s := nearFixture(t)
	defer s.Close()
	rc := s.NewReadCache()
	q := []float32{1, 0, 0}
	for range 2 { // the second round is served from the cache
		for _, c := range []struct {
			sc   sensitivity.Scope
			want string
		}{{sensitivity.Owner(), "a secret b c "}, {sensitivity.SharedScreen(), "a b c "}} {
			hs, err := rc.StatementsNear(c.sc, q, "m", 10, []string{"t:now"})
			if err != nil {
				t.Fatal(err)
			}
			if got := nearOrder(t, s, c.sc, hs); got != c.want {
				t.Errorf("%s: %q, want %q", c.sc.Name, got, c.want)
			}
		}
	}
	for _, sc := range []sensitivity.Scope{sensitivity.Owner(), sensitivity.SharedScreen()} {
		sts, _ := rc.ThreadStatuses(sc)
		direct, _ := s.ThreadStatuses(sc)
		if len(sts) != len(direct) {
			t.Errorf("%s: statuses %d cached, %d read", sc.Name, len(sts), len(direct))
		}
	}
}

// TestReadCacheDropsOnWrite: any write drops what was kept, so a change
// (a statement relabelled confidential) shows at the next read.
func TestReadCacheDropsOnWrite(t *testing.T) {
	s := nearFixture(t)
	defer s.Close()
	rc := s.NewReadCache()
	q := []float32{1, 0, 0}
	shared := sensitivity.SharedScreen()
	hs, err := rc.StatementsNear(shared, q, "m", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	var b int64
	for _, h := range hs {
		if h.Text == "statement b" {
			b = h.ID
		}
	}
	if b == 0 {
		t.Fatal("statement b not found on the shared screen")
	}
	ti1, _ := rc.TopicInfos()
	if err := s.LabelStatement(b, sensitivity.Label{Level: sensitivity.Confidential}, "v"); err != nil {
		t.Fatal(err)
	}
	hs, err = rc.StatementsNear(shared, q, "m", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hs {
		if h.ID == b {
			t.Error("relabelled confidential, but still served to the shared screen")
		}
	}
	if _, err := s.NewThread("Budget", "", sensitivity.Internal); err != nil {
		t.Fatal(err)
	}
	if ti2, _ := rc.TopicInfos(); len(ti2) != len(ti1)+1 {
		t.Errorf("a thread was added: %d topics, then %d", len(ti1), len(ti2))
	}
}
