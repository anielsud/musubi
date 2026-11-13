package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

// The app and the command line read sessions through one Connectors: the
// recorder's sessions before since come in again when Musubi has them.
func TestConnectorsReadKnownEarlierSessions(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.DefaultConfig()
	cfg.Recorder.OfRecord = "musubi"
	cfg.Recorder.Data = filepath.Join(dir, "rec")
	cfg.Recorder.Since = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if err := st.PutManifest(tomoe.Name, "known-session", "h/v", "application/x-tomoe-session+json"); err != nil {
		t.Fatal(err)
	}
	var c tomoe.Connector
	for _, x := range Connectors(cfg, st, filepath.Join(dir, "inbox")) {
		if tc, ok := x.(tomoe.Connector); ok {
			c = tc
		}
	}
	if c.Known == nil || !c.Known("known-session") || c.Known("unknown-session") {
		t.Fatalf("sessions connector without the Known rule: %+v", c)
	}
}
