package recording

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/control"
	"github.com/anielsud/musubi/internal/recorder/videohint"
)

// toLookView is the Musubi app's view of a videohint.Look: one capture
// of the meeting window and what was found in it, for the hint timeline.
// Ring details go over as they are recorded, for showing.
func toLookView(l videohint.Look, start time.Time, withThumb bool) control.LookView {
	v := control.LookView{
		ID: l.ID, Time: l.Time.Format(time.RFC3339Nano), SessionTime: l.Time.Sub(start).Seconds(),
		Stage: string(l.Stage), Detail: l.Detail, Window: l.Window, Name: l.Name, FromCache: l.FromCache, Usable: l.Usable,
		Candidates: l.Candidates, Width: l.Width, Height: l.Height, ThumbOf: l.ThumbOf,
	}
	if l.Ring != nil {
		v.Ring, _ = json.Marshal(l.Ring)
	}
	for _, r := range l.Rings {
		if b, err := json.Marshal(r); err == nil {
			v.Rings = append(v.Rings, b)
		}
	}
	if withThumb && len(l.Thumb) > 0 {
		v.Thumb = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(l.Thumb)
	}
	return v
}

// Looks is a session's looks at the meeting window, oldest first,
// without thumbnails (see LookThumb): the current recording's if id is
// "" or its ID, else a saved session's.
func (a *Recorder) Looks(id string) ([]control.LookView, error) {
	a.videoHintMu.Lock()
	if hs := a.hints; hs != nil && (id == "" || id == hs.sess.ID) {
		out := make([]control.LookView, len(a.videoLooks))
		for i, l := range a.videoLooks {
			out[i] = toLookView(l, hs.sess.CreatedAt, false)
		}
		a.videoHintMu.Unlock()
		return out, nil
	}
	a.videoHintMu.Unlock()
	if id == "" {
		return []control.LookView{}, nil
	}
	sess, err := a.loadSession(id)
	if err != nil {
		return nil, err
	}
	looks, err := videohint.ReadLooks(filepath.Join(config.SessionDir(), id))
	if err != nil {
		return nil, err
	}
	out := make([]control.LookView, len(looks))
	for i, l := range looks {
		out[i] = toLookView(l, sess.CreatedAt, false)
	}
	return out, nil
}

// lookDir is the folder of session id's looks ("" is the current
// recording).
func (a *Recorder) lookDir(id string) (string, error) {
	if id == "" {
		a.videoHintMu.Lock()
		if a.hints != nil {
			id = a.hints.sess.ID
		}
		a.videoHintMu.Unlock()
	}
	return sessionDir(id)
}

// LookThumb is a look's thumbnail, a JPEG ("" for the current recording).
// A look that shares another's thumbnail (ThumbOf) is the caller's to
// follow.
func (a *Recorder) LookThumb(id string, look int) ([]byte, error) {
	dir, err := a.lookDir(id)
	if err != nil {
		return nil, err
	}
	return videohint.ReadThumb(dir, look)
}

// LookTile is a kept tile crop, a JPEG ("" for the current recording).
func (a *Recorder) LookTile(id string, look int) ([]byte, error) {
	dir, err := a.lookDir(id)
	if err != nil {
		return nil, err
	}
	return videohint.ReadTile(dir, look)
}
