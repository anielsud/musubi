package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/store"
)

// Once Musubi's recorder is of record, calendar context is written into
// the recorder's copy of a session only; Tomoe's folder is never written.
func TestWriteTomoeContextNeverWritesTomoeAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db", "musubi.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	for _, it := range []item.Item{
		{ID: "tomoe:aaaa", Source: "tomoe", Ref: "aaaa", Kind: item.KindMeeting, Title: "rec", OccurredAt: at, EndedAt: at.Add(time.Hour)},
		{ID: "tomoe:bbbb", Source: "tomoe", Ref: "bbbb", Kind: item.KindMeeting, Title: "rec", OccurredAt: at, EndedAt: at.Add(time.Hour)},
		{ID: "cal:1", Source: "m365", Ref: "1", Kind: item.KindEvent, Title: "Planning", OccurredAt: at, EndedAt: at.Add(time.Hour),
			Participants: []item.Participant{{Name: "Kai Moreno", Email: "kai@example.com"}}},
	} {
		if _, err := st.UpsertItem(it); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceLinks("occurred_as", []store.Link{{ItemID: "tomoe:aaaa", OtherID: "cal:1"}, {ItemID: "tomoe:bbbb", OtherID: "cal:1"}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.TomoeSessions = filepath.Join(dir, "tomoe")
	cfg.Recorder.Data = filepath.Join(dir, "rec")
	cfg.Recorder.OfRecord = "musubi"
	// aaaa is in both folders (copied over); bbbb only in Tomoe's.
	for _, d := range []string{"tomoe/aaaa", "tomoe/bbbb", "rec/sessions/aaaa"} {
		os.MkdirAll(filepath.Join(dir, d), 0o700)
		os.WriteFile(filepath.Join(dir, d, "session.json"), []byte("{}"), 0o600)
	}
	e := &Engine{Cfg: cfg, Store: st}
	for _, off := range []string{"", "off"} {
		e.Cfg.Recorder.Tomoe = off
		if _, err := e.WriteTomoeContext(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "rec", "sessions", "aaaa", "calendar.json")); err != nil {
			t.Errorf("tomoe %q: no calendar.json in the recorder's copy", off)
		}
		for _, id := range []string{"aaaa", "bbbb"} {
			if ents, _ := os.ReadDir(filepath.Join(dir, "tomoe", id)); len(ents) != 1 {
				t.Errorf("tomoe %q: Tomoe's %s written: %v", off, id, ents)
			}
		}
	}
	// Before the switch it is Tomoe's folder, as before.
	e.Cfg.Recorder.OfRecord, e.Cfg.Recorder.Tomoe = "tomoe", ""
	if _, err := e.WriteTomoeContext(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tomoe", "bbbb", "calendar.json")); err != nil {
		t.Error("before the switch, Tomoe's session got no calendar.json")
	}
}
