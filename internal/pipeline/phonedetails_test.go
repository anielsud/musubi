package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/config"
	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/provider"
	"github.com/anielsud/musubi/internal/raw"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/tomoe"
	"github.com/anielsud/musubi/internal/store"
)

// A phone recording's details (docs/phone.md, "Recording details"): the
// notes are searchable passages of the meeting under the meeting's
// label, a later rev replaces them through a new raw version, and no
// photo (bytes, name or hash) reaches an item or a passage, which is
// everything a stage or a model is given.
func TestPhoneDetailsIngested(t *testing.T) {
	const sid = "11111111-0000-4000-8000-000000000004"
	itemID := "tomoe:" + sid
	sessions := t.TempDir()
	dst := filepath.Join(sessions, sid)
	os.MkdirAll(filepath.Join(dst, "photos"), 0o700)
	b, err := os.ReadFile(filepath.Join(corpusDir(), "tomoe", sid, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dst, "session.json"), b, 0o600)
	photoSHA := strings.Repeat("cd", 32)
	os.WriteFile(filepath.Join(dst, "photos", "photo-0001.jpg"), []byte("PHOTO-BYTES-MARKER"), 0o600)
	writeDetails := func(rev int, notes string) {
		d := map[string]any{"writer": "musubi", "recording": "1b4e28ba-2fa1-41d2-883f-0016d3cca427", "rev": rev,
			"attendees": []map[string]string{{"name": "Quinn Placeholder"}},
			"notes":     map[string]string{"text": notes, "updated": "2026-11-17T10:20:00+01:00"},
			"photos":    []map[string]any{{"name": "photo-0001.jpg", "size": 18, "sha256": photoSHA}}}
		b, _ := json.Marshal(d)
		if err := os.WriteFile(filepath.Join(dst, "phone-details.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeDetails(1, "Zebrafish procurement question for the vendor")

	cfg, err := config.Load(filepath.Join(corpusDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rs, err := raw.Open(cfg.RawDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: cfg, Store: st, Raw: rs, Embedders: []provider.Embedder{provider.Hash{Dim: 64}},
		CuratedPath: filepath.Join(corpusDir(), "registry.toml"),
		Connectors:  []source.Connector{tomoe.Connector{Dir: sessions, PhoneDetails: true}}}
	run(t, r)

	search := func(q string) []store.Hit {
		hits, err := st.Search(sensitivity.Owner(), store.SearchQuery{Text: q, K: 20, Mode: store.ModeKeyword})
		if err != nil {
			t.Fatal(err)
		}
		return hits
	}
	if !hasItem(search("zebrafish procurement"), itemID) {
		t.Fatal("notes not searchable")
	}
	itemLabel, _, err := st.ItemLabel(itemID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := st.Item(sensitivity.Owner(), itemID)
	if err != nil || v == nil {
		t.Fatal(err)
	}
	notes := 0
	for _, h := range v.Chunks {
		if strings.Contains(h.Text, "[your notes]") {
			notes++
			if h.Label.Level != itemLabel.Level {
				t.Errorf("notes passage label %v, meeting %v", h.Label.Level, itemLabel.Level)
			}
		}
	}
	if notes != 1 {
		t.Errorf("%d notes passages", notes)
	}
	noPhoto := func() {
		t.Helper()
		items, _ := st.AllItems()
		for _, it := range items {
			b, _ := json.Marshal(it)
			if s := string(b); strings.Contains(s, "photo-0001") || strings.Contains(s, photoSHA) || strings.Contains(s, "PHOTO-BYTES") {
				t.Errorf("%s carries a photo", it.ID)
			}
		}
		cs, _ := st.Chunks(itemID)
		for _, c := range cs {
			if strings.Contains(c.Text, "photo-0001") || strings.Contains(c.Text, "PHOTO-BYTES") {
				t.Error("a passage carries a photo")
			}
		}
	}
	noPhoto()
	items, _ := st.AllItems()
	for _, it := range items {
		if it.ID == itemID && !strings.Contains(fmtParts(it.Participants), "Quinn Placeholder/attendee") {
			t.Errorf("listed person missing: %s", fmtParts(it.Participants))
		}
	}

	// A later rev replaces the notes.
	writeDetails(2, "Walrus logistics instead")
	s := run(t, r)
	if s.Changed != 1 {
		t.Errorf("changed %d items", s.Changed)
	}
	if hasItem(search("zebrafish procurement"), itemID) || !hasItem(search("walrus logistics"), itemID) {
		t.Error("rev 2 didn't replace rev 1's notes")
	}
	noPhoto()
}

func fmtParts(ps []item.Participant) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name+"/"+p.Role)
	}
	return strings.Join(out, ",")
}
