package tomoe

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/source"
)

func TestSinceSkipsEarlierSessions(t *testing.T) {
	dir := t.TempDir()
	for id, at := range map[string]string{"old": "2026-11-10T10:00:00Z", "edge": "2026-11-11T09:00:00Z", "new": "2026-11-11T11:00:00Z"} {
		os.MkdirAll(filepath.Join(dir, id), 0o700)
		os.WriteFile(filepath.Join(dir, id, "session.json"), []byte(`{"id":"`+id+`","created_at":"`+at+`"}`), 0o600)
	}
	fetch := func(c Connector) []string {
		var refs []string
		if err := c.Fetch(context.Background(), func(r source.Record) error { refs = append(refs, r.Ref); return nil }); err != nil {
			t.Fatal(err)
		}
		sort.Strings(refs)
		return refs
	}
	if got := fetch(Connector{Dir: dir}); len(got) != 3 {
		t.Errorf("no since: %v", got)
	}
	got := fetch(Connector{Dir: dir, Since: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)})
	if len(got) != 1 || got[0] != "new" {
		t.Errorf("since: %v, want [new]", got)
	}
}
