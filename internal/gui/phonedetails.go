package gui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strings"

	"github.com/anielsud/musubi/internal/phonedetails"
	"github.com/anielsud/musubi/internal/sensitivity"
	"github.com/anielsud/musubi/internal/store"
)

// notesLine starts a notes line in a passage (stages.Chunk).
const notesLine = "You: [your notes] "

// visibleNotes is the notes as the visible passages hold them.
func visibleNotes(chunks []store.Hit) string {
	var lines []string
	for _, c := range chunks {
		for _, l := range strings.Split(c.Text, "\n") {
			if t, ok := strings.CutPrefix(l, notesLine); ok {
				lines = append(lines, t)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// A phone recording's details on the meeting page (docs/phone.md,
// "Recording details"): the owner's notes and the people present, under
// the meeting's label and only where the meeting is visible in the
// page's scope; the photos, owner-only, only in the owner's scope. Read
// from the session folder's phone-details.json (never written here).

// MeetingDetails is what the meeting page shows.
type MeetingDetails struct {
	Notes        string                  `json:"notes"`
	NotesUpdated string                  `json:"notesUpdated,omitempty"`
	People       []phonedetails.Attendee `json:"people"`
	// Label is the meeting's, which the notes and people carry.
	Label sensitivity.Label `json:"label"`
	Share sensitivity.Share `json:"share"`
	// Photos are listed in the owner's scope only; PhotosHidden counts
	// those left out in another.
	Photos       []MeetingPhotoRef `json:"photos"`
	PhotosHidden int               `json:"photosHidden,omitempty"`
}

type MeetingPhotoRef struct {
	Name  string `json:"name"`
	Taken string `json:"taken,omitempty"`
	Size  int64  `json:"size"`
}

// photoShare is the photos' label: personal, whatever the meeting's.
const photoShare = sensitivity.ShareOwner

// meetingDetailsFile is itemID's details, when the meeting is a
// recording with some and it is visible in scope.
func (a *App) meetingDetailsFile(itemID, scope string) (*phonedetails.File, *MeetingDetails, error) {
	if a.eng == nil {
		return nil, nil, a.notReady()
	}
	if !a.eng.Cfg.Phone.Details {
		return nil, nil, nil
	}
	sid, ok := sessionOfItem(itemID)
	if !ok {
		return nil, nil, nil
	}
	sc, err := a.eng.Scope(scope)
	if err != nil {
		return nil, nil, err
	}
	// The meeting's own scoped read decides whether its details show.
	v, err := a.eng.Store.Item(sc, itemID)
	if err != nil || v == nil {
		return nil, nil, err
	}
	dir := a.eng.Cfg.SessionDir(sid)
	if dir == "" {
		return nil, nil, nil
	}
	f := phonedetails.Read(dir)
	if f == nil || f.Empty() {
		return nil, nil, nil
	}
	m := &MeetingDetails{Notes: f.Notes.Text, NotesUpdated: f.Notes.Updated, People: f.Attendees, Label: v.Label,
		Share: sensitivity.ShareOf(v.Label), Photos: []MeetingPhotoRef{}}
	if m.People == nil {
		m.People = []phonedetails.Attendee{}
	}
	if sc.Name != sensitivity.Owner().Name {
		// Only what the scope shows of the notes: their visible passages.
		m.Notes, m.NotesUpdated = visibleNotes(v.Chunks), ""
	}
	if sc.Name == sensitivity.Owner().Name {
		for _, p := range f.Photos {
			m.Photos = append(m.Photos, MeetingPhotoRef{Name: p.Name, Taken: p.Taken, Size: p.Size})
		}
	} else {
		m.PhotosHidden = len(f.Photos)
	}
	return f, m, nil
}

// MeetingDetails is a phone recording's notes, people and photos for the
// meeting page; nil when it has none, or none to show in scope.
func (a *App) MeetingDetails(itemID, scope string) (*MeetingDetails, error) {
	_, m, err := a.meetingDetailsFile(itemID, scope)
	return m, err
}

// maxThumbPixels bounds what is decoded for a thumbnail.
const maxThumbPixels = 64 << 20

// MeetingPhoto is one listed photo as a data: URL, in the owner's scope
// only; thumb scales it to at most 320 pixels a side.
func (a *App) MeetingPhoto(itemID, name, scope string, thumb bool) (string, error) {
	if scope != sensitivity.Owner().Name {
		return "", errors.New("photos are shown in your own scope only")
	}
	f, _, err := a.meetingDetailsFile(itemID, scope)
	if err != nil || f == nil {
		return "", err
	}
	listed := false
	for _, p := range f.Photos {
		listed = listed || p.Name == name
	}
	sid, _ := sessionOfItem(itemID)
	path := phonedetails.PhotoPath(a.eng.Cfg.SessionDir(sid), name)
	if !listed || path == "" {
		return "", errors.New("no such photo")
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > phonedetails.MaxPhotoBytes {
		return "", errors.New("the photo isn't on this Mac")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if thumb {
		if t, err := thumbnail(b, 320); err == nil {
			b = t
		}
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b), nil
}

// thumbnail scales a JPEG down to fit side pixels (box average).
func thumbnail(b []byte, side int) ([]byte, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if cfg.Width*cfg.Height > maxThumbPixels {
		return nil, fmt.Errorf("photo too large to scale")
	}
	if cfg.Width <= side && cfg.Height <= side {
		return b, nil
	}
	src, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	w, h := side, side*sb.Dy()/sb.Dx()
	if sb.Dy() > sb.Dx() {
		w, h = side*sb.Dx()/sb.Dy(), side
	}
	w, h = max(w, 1), max(h, 1)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := sb.Min.Y+y*sb.Dy()/h, sb.Min.Y+(y+1)*sb.Dy()/h
		for x := 0; x < w; x++ {
			x0, x1 := sb.Min.X+x*sb.Dx()/w, sb.Min.X+(x+1)*sb.Dx()/w
			var r, g, bl, n uint64
			// Sample at most 4x4 points per box: enough for a thumbnail.
			for yy := y0; yy < y1; yy += max(1, (y1-y0)/4) {
				for xx := x0; xx < x1; xx += max(1, (x1-x0)/4) {
					cr, cg, cb, _ := src.At(xx, yy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 0xff
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
