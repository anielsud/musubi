package recording

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/session"
)

const importID = "6f1c2d3e-4b5a-4c6d-8e7f-a0b1c2d3e4f5"

// inboxFile puts a stand-in audio file in the recorder's inbox.
func inboxFile(t *testing.T, name string) string {
	t.Helper()
	dir := control.InboxDir(config.DataDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitImport(t *testing.T, r *Recorder, args control.ImportArgs, want string) control.ImportStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := r.Import(args)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("import stuck at %q, want %q", st.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeImport saves a phone session for the args, as the child would.
func fakeImport(r *Recorder) func(context.Context, control.ImportArgs) error {
	return func(_ context.Context, a control.ImportArgs) error {
		return r.store.Save(&session.Session{ID: a.ID, Title: "Phone Meeting", Platform: PlatformPhone, CreatedAt: a.StartedAt, Sources: []string{"mic"}, EventID: a.EventID})
	}
}

func TestImportArgsChecked(t *testing.T) {
	r := newTestRecorder(t)
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.FixedZone("", 3600))
	good := inboxFile(t, importID+".aac")
	outside := filepath.Join(config.DataDir(), "elsewhere.aac")
	os.WriteFile(outside, []byte("audio"), 0o600)
	link := filepath.Join(control.InboxDir(config.DataDir()), "link.aac")
	os.Symlink(outside, link)
	for name, a := range map[string]control.ImportArgs{
		"outside the inbox": {ID: importID, Path: outside, StartedAt: at},
		"climbing out":      {ID: importID, Path: filepath.Join(control.InboxDir(config.DataDir()), "..", "elsewhere.aac"), StartedAt: at},
		"relative":          {ID: importID, Path: importID + ".aac", StartedAt: at},
		"a link":            {ID: importID, Path: link, StartedAt: at},
		"missing":           {ID: importID, Path: filepath.Join(control.InboxDir(config.DataDir()), "none.aac"), StartedAt: at},
		"bad id":            {ID: "../x", Path: good, StartedAt: at},
		"upper-case id":     {ID: "6F1C2D3E-4B5A-4C6D-8E7F-A0B1C2D3E4F5", Path: good, StartedAt: at},
		"no start":          {ID: importID, Path: good},
		"bad event":         {ID: importID, Path: good, StartedAt: at, EventID: "calendar:x/../y"},
	} {
		if _, err := r.Import(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An id already used by a recorded meeting is never touched.
	r.store.Save(&session.Session{ID: importID, Title: "Teams Meeting", Platform: "Teams"})
	if _, err := r.Import(control.ImportArgs{ID: importID, Path: good, StartedAt: at}); err == nil {
		t.Error("import over a recorded meeting accepted")
	}
}

func TestImportRuns(t *testing.T) {
	r := newTestRecorder(t)
	defer r.stopImports()
	r.imports.run = fakeImport(r)
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.FixedZone("", 3600))
	args := control.ImportArgs{ID: importID, Path: inboxFile(t, importID+".aac"), StartedAt: at, EventID: "calendar:abc"}
	st := waitImport(t, r, args, control.ImportImported)
	if st.Session != importID {
		t.Errorf("session %q", st.Session)
	}
	if _, err := os.Stat(args.Path); err == nil {
		t.Error("inbox file kept after the import")
	}
	// After a restart the saved session answers.
	r2 := New(r.cfg)
	if st, err := r2.Import(args); err != nil || st.State != control.ImportImported {
		t.Errorf("after restart: %+v %v", st, err)
	}
}

func TestImportWaitsForMeetings(t *testing.T) {
	old, oldPoll := relearnWait, importPoll
	relearnWait, importPoll = 10*time.Millisecond, 10*time.Millisecond
	defer func() { relearnWait, importPoll = old, oldPoll }()
	f := newFakeRecorder(t)
	defer f.stopImports()
	var mu sync.Mutex
	runs, cut := 0, 0
	started := make(chan struct{}, 4)
	f.imports.run = func(ctx context.Context, a control.ImportArgs) error {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		started <- struct{}{}
		if n == 1 {
			<-ctx.Done() // the first run is stopped by a meeting
			mu.Lock()
			cut++
			mu.Unlock()
			return ctx.Err()
		}
		return fakeImport(f.Recorder)(ctx, a)
	}
	// Queued during a meeting: nothing runs until it is saved.
	if err := f.StartSession("mic", "", "en", "Teams"); err != nil {
		t.Fatal(err)
	}
	args := control.ImportArgs{ID: importID, Path: inboxFile(t, importID+".aac"), StartedAt: time.Now()}
	if st, err := f.Import(args); err != nil || st.State != control.ImportWaiting {
		t.Fatalf("during a meeting: %+v %v", st, err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	ran := runs
	mu.Unlock()
	if ran != 0 {
		t.Fatal("import ran during a meeting")
	}
	if _, err := f.StopSession(); err != nil {
		t.Fatal(err)
	}
	<-started
	// A meeting starting mid-import stops it; it waits, then starts over.
	if err := f.StartSession("mic", "", "en", "Teams"); err != nil {
		t.Fatal(err)
	}
	waitImport(t, f.Recorder, args, control.ImportWaiting)
	mu.Lock()
	if cut != 1 || runs != 1 {
		t.Errorf("runs %d, cut %d", runs, cut)
	}
	mu.Unlock()
	if _, err := f.StopSession(); err != nil {
		t.Fatal(err)
	}
	waitImport(t, f.Recorder, args, control.ImportImported)
}

func TestImportFailureAndRetry(t *testing.T) {
	r := newTestRecorder(t)
	defer r.stopImports()
	fail := true
	var mu sync.Mutex
	r.imports.run = func(ctx context.Context, a control.ImportArgs) error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return errors.New("models crashed")
		}
		return fakeImport(r)(ctx, a)
	}
	args := control.ImportArgs{ID: importID, Path: inboxFile(t, importID+".aac"), StartedAt: time.Now()}
	st := waitImport(t, r, args, control.ImportFailed)
	if st.Error == "" || st.Error == "models crashed" {
		t.Errorf("error shown: %q", st.Error)
	}
	if _, err := os.Stat(args.Path); err != nil {
		t.Error("inbox file dropped after a failure")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if st, _ := r.Import(args); st.State != control.ImportFailed {
		t.Errorf("asked again without retry: %s", st.State)
	}
	args.Retry = true
	r.Import(args)
	args.Retry = false
	waitImport(t, r, args, control.ImportImported)
}

// The op goes through the control socket like the others.
func TestImportOverControl(t *testing.T) {
	c := newControlled(t)
	defer c.stopImports()
	c.imports.run = fakeImport(c.Recorder)
	args := control.ImportArgs{ID: importID, Path: inboxFile(t, importID+".aac"), StartedAt: time.Now()}
	var st control.ImportStatus
	if err := c.call(t, control.OpImport, args, &st); err != nil || st.Session != importID {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.call(t, control.OpImport, control.ImportArgs{Path: "/etc/passwd", StartedAt: time.Now()}, &st); err == nil {
		t.Error("a file outside the inbox accepted over the socket")
	}
}
