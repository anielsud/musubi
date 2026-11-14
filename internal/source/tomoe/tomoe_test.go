package tomoe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/dataformat"
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
	// A migrated meeting Musubi already has comes in again (a speaker
	// fixed in it); an unknown earlier one (a shadow recording) doesn't.
	got = fetch(Connector{Dir: dir, Since: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), Known: func(ref string) bool { return ref == "old" }})
	if len(got) != 2 || got[0] != "new" || got[1] != "old" {
		t.Errorf("since + known: %v, want [new old]", got)
	}
}

// Sessions in a recorder folder a newer Musubi wrote aren't read.
func TestNewerRecorderFolderRefused(t *testing.T) {
	data := t.TempDir()
	dir := filepath.Join(data, "sessions")
	os.MkdirAll(filepath.Join(dir, "s1"), 0o700)
	os.WriteFile(filepath.Join(dir, "s1", "session.json"), []byte(`{"id":"s1"}`), 0o600)
	os.WriteFile(filepath.Join(data, "format"), []byte("99\n"), 0o600)
	n := 0
	err := Connector{Dir: dir}.Fetch(context.Background(), func(source.Record) error { n++; return nil })
	if !errors.Is(err, dataformat.ErrNewer) || n != 0 {
		t.Fatalf("%d read, %v", n, err)
	}
}

// A session folder with phone sync's details is read with them, only
// when PhoneDetails is on; other folders are read as before.
func TestPhoneDetailsWrapped(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "11111111-0000-4000-8000-0000000000a1")
	phone := filepath.Join(dir, "11111111-0000-4000-8000-0000000000a2")
	session := []byte(`{"title":"x","created_at":"2026-11-17T10:00:00Z","segments":[]}`)
	for _, d := range []string{plain, phone} {
		os.MkdirAll(d, 0o700)
		os.WriteFile(filepath.Join(d, "session.json"), session, 0o600)
	}
	os.WriteFile(filepath.Join(phone, "phone-details.json"), []byte(`{"writer":"musubi","rev":1,"attendees":[],"notes":{"text":"n"},"photos":[]}`), 0o600)
	os.MkdirAll(filepath.Join(phone, "photos"), 0o700)
	os.WriteFile(filepath.Join(phone, "photos", "photo-0001.jpg"), []byte("PHOTO-BYTES"), 0o600)
	for _, on := range []bool{false, true} {
		got := map[string]string{}
		err := Connector{Dir: dir, PhoneDetails: on}.Fetch(context.Background(), func(r source.Record) error {
			got[r.Ref] = string(r.Data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got[filepath.Base(plain)] != string(session) {
			t.Errorf("on=%v: plain session changed: %s", on, got[filepath.Base(plain)])
		}
		p := got[filepath.Base(phone)]
		if on != strings.Contains(p, `"phone_details"`) || strings.Contains(p, "PHOTO-BYTES") {
			t.Errorf("on=%v: phone session %s", on, p)
		}
	}
}
