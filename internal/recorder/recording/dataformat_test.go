package recording

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/dataformat"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/voices"
)

// A recorder refused its folder (a newer Musubi wrote it) says so in its
// status, records nothing, and refuses every op but the status, sources
// and tools.
func TestRefusedFolder(t *testing.T) {
	c := newControlled(t)
	c.cfg.Meeting.LiveFeed = false
	why := errors.New("the recorder's folder was written by a newer Musubi (format 9)")
	c.Refuse(why)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var st control.Status
	c.must(t, control.OpStatus, nil, &st)
	if !strings.Contains(strings.Join(st.Health, "\n"), "Not recording: the recorder's folder was written by a newer Musubi") {
		t.Errorf("health %q", st.Health)
	}
	for _, op := range []string{control.OpStart, control.OpVoices, control.OpLearnSaved, control.OpReloadConfig, control.OpDelete} {
		if err := c.call(t, op, nil, nil); err == nil || !strings.Contains(err.Error(), "newer Musubi") {
			t.Errorf("%s: %v", op, err)
		}
	}
	if err := c.StartSession("", "", "en", "Zoom"); !errors.Is(err, why) {
		t.Errorf("StartSession: %v", err)
	}
	if len(c.began) != 0 {
		t.Errorf("began %v", c.began)
	}
	c.Shutdown()
}

// A voices file a newer Musubi wrote is a health line; recording isn't
// refused.
func TestNewerVoicesFileHealth(t *testing.T) {
	r := newTestRecorder(t)
	path := voices.Path(config.DataDir(), models.SpeakerModels[0].ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.checkVoiceFiles()
	if h := strings.Join(r.Status().Health, "\n"); !strings.Contains(h, "written by a newer Musubi: meetings are recorded") {
		t.Errorf("health %q", h)
	}
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.checkVoiceFiles()
	if h := r.Status().Health; len(h) != 0 && strings.Contains(strings.Join(h, "\n"), "newer Musubi") {
		t.Errorf("health kept %q", h)
	}
	if r.refused != nil || dataformat.Check(config.DataDir()) != nil {
		t.Error("a newer voices file refused the folder")
	}
}
