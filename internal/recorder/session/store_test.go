package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreWritesTomoeLayout(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	sess := &Session{ID: "9f1c-22", Title: "Plan", CreatedAt: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		Segments: []Segment{{ID: "1", Speaker: "You", Text: "ok", Source: "mic"}}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "9f1c-22", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(sess, "", "  ")
	if string(data) != string(want) {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}

	got, err := st.Load("9f1c-22")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Plan" || len(got.Segments) != 1 || got.Segments[0].Text != "ok" {
		t.Errorf("loaded %+v", got)
	}

	sess.Title = "Plan, renamed"
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Load("9f1c-22"); got == nil || got.Title != "Plan, renamed" {
		t.Errorf("after resave: %+v", got)
	}
}

func TestStoreShortIDs(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, id := range []string{"aa11", "aa22", "bb33"} {
		if err := st.Save(&Session{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := st.Load("bb"); err != nil || s.ID != "bb33" {
		t.Errorf("unique prefix: %v, %v", s, err)
	}
	if _, err := st.Load("aa"); err == nil {
		t.Error("ambiguous prefix loaded")
	}
	if _, err := st.Load("cc"); err == nil {
		t.Error("unknown ID loaded")
	}
	if err := st.Delete("aa2"); err != nil {
		t.Fatal(err)
	}
	if s, err := st.Load("aa"); err != nil || s.ID != "aa11" {
		t.Errorf("after delete: %v, %v", s, err)
	}
	if err := st.Delete("zz"); err == nil {
		t.Error("deleting an unknown ID succeeded")
	}
}

// An ID that is itself a prefix of another still loads its own session.
func TestStoreExactIDBeatsPrefix(t *testing.T) {
	st := NewStore(t.TempDir())
	for _, id := range []string{"ab", "abc"} {
		if err := st.Save(&Session{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := st.Load("ab"); err != nil || s.ID != "ab" {
		t.Errorf("Load(ab) = %v, %v", s, err)
	}
}

func TestStoreDeleteRemovesDirectory(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	if err := st.Save(&Session{ID: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gone", "audio.m4a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
		t.Errorf("directory still there: %v", err)
	}
}

func TestStoreListNewestFirstSkippingDamage(t *testing.T) {
	root := t.TempDir()
	st := NewStore(root)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"mid", "old", "new"} {
		offset := []time.Duration{time.Hour, 0, 2 * time.Hour}[i]
		if err := st.Save(&Session{ID: id, CreatedAt: base.Add(offset)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", "session.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range list {
		got = append(got, s.ID)
	}
	if len(got) != 3 || got[0] != "new" || got[1] != "mid" || got[2] != "old" {
		t.Errorf("List = %v", got)
	}
}

func TestStoreListWithoutSessions(t *testing.T) {
	for _, root := range []string{t.TempDir(), filepath.Join(t.TempDir(), "never-made")} {
		list, err := NewStore(root).List()
		if err != nil || len(list) != 0 {
			t.Errorf("List(%s) = %v, %v", root, list, err)
		}
	}
}
