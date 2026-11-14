package phone

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/phonedetails"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// jpegish is a stand-in photo: the Mac checks size and hash, not pixels.
func jpegish(seed byte, n int) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	for i := 0; i < n; i++ {
		b = append(b, seed+byte(i))
	}
	return b
}

// metaV2 is metaJSON with version 2 and details: notes, people and
// photos (name → bytes).
func metaV2(id string, rev int, notes string, people []string, photos map[string][]byte, chunks ...[]byte) []byte {
	var m map[string]any
	json.Unmarshal(metaJSON(id, chunks...), &m)
	m["version"] = 2
	att := []map[string]any{}
	for _, p := range people {
		att = append(att, map[string]any{"name": p})
	}
	ph := []map[string]any{}
	for _, name := range sortedNames(photos) {
		ph = append(ph, map[string]any{"name": name, "size": len(photos[name]), "sha256": sum(photos[name]), "taken": "2026-11-17T10:30:00.000+01:00"})
	}
	m["details"] = map[string]any{"rev": rev, "attendees": att, "notes": map[string]any{"text": notes, "updated": "2026-11-17T10:40:00.000+01:00"}, "photos": ph}
	b, _ := json.Marshal(m)
	return b
}

func sortedNames(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (p *fakePhone) addV2(rid string, rev int, notes string, people []string, photos map[string][]byte, chunks ...[]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.recs[rid]; !ok {
		p.order = append(p.order, rid)
	}
	p.recs[rid] = &fakeRec{meta: metaV2(rid, rev, notes, people, photos, chunks...), chunks: chunks, files: photos}
}

// detailsHarness has the recorder import at once into a session folder
// named after the recording, as the real recorder does.
func detailsHarness(t *testing.T) (*harness, *fakePhone, string) {
	h := newHarness(t)
	h.s.cfg.Details = true
	h.rec.set(true, control.ImportImported)
	p := newFakePhone(t)
	h.pairWith(p)
	sess := filepath.Join(h.dir, "recorder", "sessions", rid)
	if err := os.MkdirAll(sess, 0o700); err != nil {
		t.Fatal(err)
	}
	return h, p, sess
}

func lastReport(p *fakePhone) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reports) == 0 {
		return nil
	}
	return p.reports[len(p.reports)-1]
}

func TestDetailsFetchedAndWritten(t *testing.T) {
	h, p, sess := detailsHarness(t)
	ph1, ph2 := jpegish(1, 300), jpegish(2, 500)
	notes := "Synthetic note: badge scanner pricing\nsecond line"
	p.addV2(rid, 3, notes, []string{"Ana Example", "Bo Sample"}, map[string][]byte{"photo-0001.jpg": ph1, "photo-0002.jpg": ph2}, adts(2, 30, 1))
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list chunk file file state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	r := lastReport(p)
	if r["state"] != "imported" || r["details_rev"] != float64(3) {
		t.Errorf("report %v", r)
	}
	f := phonedetails.Read(sess)
	if f == nil || f.Rev != 3 || f.Recording != rid || f.Notes.Text != notes || len(f.Attendees) != 2 || len(f.Photos) != 2 {
		t.Fatalf("details %+v", f)
	}
	for name, want := range map[string][]byte{"photo-0001.jpg": ph1, "photo-0002.jpg": ph2} {
		b, err := os.ReadFile(filepath.Join(sess, "photos", name))
		if err != nil || !bytes.Equal(b, want) {
			t.Errorf("%s: %d bytes (%v)", name, len(b), err)
		}
		if fi, _ := os.Stat(filepath.Join(sess, "photos", name)); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", name, fi.Mode())
		}
	}
	if fi, _ := os.Stat(filepath.Join(sess, phonedetails.FileName)); fi.Mode().Perm() != 0o600 {
		t.Errorf("details mode %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(h.s.recDir(rid), "photos")); err == nil {
		t.Error("staged photos kept once written")
	}
	if strings.Contains(h.logged(), "badge scanner") || strings.Contains(h.logged(), "Ana Example") {
		t.Error("notes or names in the log")
	}
}

func TestDetailsLaterRevReplaces(t *testing.T) {
	h, p, sess := detailsHarness(t)
	ph1, ph2, ph3 := jpegish(1, 300), jpegish(2, 500), jpegish(3, 200)
	p.addV2(rid, 1, "first", []string{"Ana Example"}, map[string][]byte{"photo-0001.jpg": ph1, "photo-0002.jpg": ph2}, adts(2, 30, 1))
	h.syncOnce(p)
	p.states[rid] = "imported"
	// The phone dropped its photos once the Mac had rev 1; then the owner
	// deleted one, added another and rewrote the notes. The phone lists
	// the recording again (imported, newer details): only the new photo
	// is fetched, no chunk.
	p.addV2(rid, 4, "second", []string{"Bo Sample"}, map[string][]byte{"photo-0002.jpg": ph2, "photo-0003.jpg": ph3}, adts(2, 30, 1))
	p.mu.Lock()
	delete(p.recs[rid].files, "photo-0002.jpg")
	p.mu.Unlock()
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list file state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if r := lastReport(p); r["state"] != "imported" || r["details_rev"] != float64(4) {
		t.Errorf("report %v", r)
	}
	f := phonedetails.Read(sess)
	if f == nil || f.Rev != 4 || f.Notes.Text != "second" || f.Attendees[0].Name != "Bo Sample" || len(f.Photos) != 2 {
		t.Fatalf("details %+v", f)
	}
	ents, _ := os.ReadDir(filepath.Join(sess, "photos"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "photo-0002.jpg,photo-0003.jpg" {
		t.Errorf("photos %v", names)
	}
	// Nothing newer: nothing fetched or reported.
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list state agenda bye" {
		// The fake phone lists imported recordings every time; the Mac
		// repeats its applied rev rather than fetching anything.
		t.Errorf("ops %s", ops)
	}
}

func TestDetailsDamagedPhotoRetried(t *testing.T) {
	h, p, sess := detailsHarness(t)
	ph1, ph2 := jpegish(1, 300), jpegish(2, 500)
	p.addV2(rid, 2, "n", nil, map[string][]byte{"photo-0001.jpg": ph1, "photo-0002.jpg": ph2}, adts(2, 30, 1))
	p.fault[rid+"/photo-0002.jpg"] = "bytes"
	h.syncOnce(p)
	if r := lastReport(p); r["state"] != "imported" || r["details_rev"] != nil {
		t.Errorf("report %v", r)
	}
	if phonedetails.Read(sess) != nil {
		t.Error("details written without a photo")
	}
	// Resumed: the photo staged before isn't fetched again.
	delete(p.fault, rid+"/photo-0002.jpg")
	p.states[rid] = "imported"
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list file state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if f := phonedetails.Read(sess); f == nil || len(f.Photos) != 2 {
		t.Errorf("details %+v", f)
	}
}

func TestDetailsGonePhotoLeftOut(t *testing.T) {
	h, p, sess := detailsHarness(t)
	p.addV2(rid, 1, "n", nil, map[string][]byte{"photo-0001.jpg": jpegish(1, 30)}, adts(2, 30, 1))
	p.fault[rid+"/photo-0001.jpg"] = "gone"
	h.syncOnce(p)
	f := phonedetails.Read(sess)
	if f == nil || len(f.Photos) != 0 || lastReport(p)["details_rev"] != float64(1) {
		t.Errorf("details %+v, report %v", f, lastReport(p))
	}
}

func TestDetailsWaitForImport(t *testing.T) {
	h, p, sess := detailsHarness(t)
	h.rec.set(true, control.ImportTranscribing)
	p.addV2(rid, 1, "n", []string{"Ana Example"}, map[string][]byte{"photo-0001.jpg": jpegish(1, 30)}, adts(2, 30, 1))
	h.syncOnce(p)
	if r := lastReport(p); r["state"] != "transcribing" || r["details_rev"] != nil {
		t.Errorf("report %v", r)
	}
	if phonedetails.Read(sess) != nil {
		t.Error("details before the meeting was saved")
	}
	h.rec.set(true, control.ImportImported)
	p.ops = nil
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list file state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if f := phonedetails.Read(sess); f == nil || f.Rev != 1 {
		t.Errorf("details %+v", f)
	}
}

func TestDetailsOff(t *testing.T) {
	h, p, sess := detailsHarness(t)
	h.s.cfg.Details = false
	p.addV2(rid, 1, "n", nil, map[string][]byte{"photo-0001.jpg": jpegish(1, 30)}, adts(2, 30, 1))
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list chunk state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if r := lastReport(p); r["details_rev"] != nil {
		t.Errorf("report %v", r)
	}
	if _, err := os.Stat(filepath.Join(sess, phonedetails.FileName)); err == nil {
		t.Error("details written with [phone] details off")
	}
}

func TestDetailsBadNamesRefused(t *testing.T) {
	h, p, sess := detailsHarness(t)
	var m map[string]any
	json.Unmarshal(metaV2(rid, 1, "n", nil, map[string][]byte{"photo-0001.jpg": jpegish(1, 30)}, adts(2, 30, 1)), &m)
	m["details"].(map[string]any)["photos"].([]any)[0].(map[string]any)["name"] = "../../evil.jpg"
	b, _ := json.Marshal(m)
	p.mu.Lock()
	p.recs[rid] = &fakeRec{meta: b, chunks: [][]byte{adts(2, 30, 1)}}
	p.order = append(p.order, rid)
	p.mu.Unlock()
	h.syncOnce(p)
	if ops := strings.Join(p.opList(), " "); ops != "hello list chunk state agenda bye" {
		t.Errorf("ops %s", ops)
	}
	if phonedetails.Read(sess) != nil {
		t.Error("details written from a bad meta")
	}
	if !strings.Contains(h.logged(), "details skipped") {
		t.Error("not logged")
	}
}
