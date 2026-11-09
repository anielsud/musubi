package engine

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anielsud/musubi/internal/item"
	"github.com/anielsud/musubi/internal/sensitivity"
)

// Headshots and voice clips come from Tomoe's saved sessions (its session
// files are the integration surface; nothing of Tomoe's is changed):
//   - a headshot is the active-speaker tile of a look whose name read is
//     the person's, cropped from the look's thumbnail (the largest such
//     tile across sessions, so the clearest picture);
//   - a voice clip is a stretch of a recording Tomoe attributed to them.

// faceCandidate is a look whose highlighted tile is someone's.
type faceCandidate struct {
	thumb      string  // looks/<id>.jpg
	x, y, w, h float64 // the tile, as a share of the window
	area       float64 // the tile's size in window pixels
}

type faceIndex struct {
	mu   sync.Mutex
	sig  string
	best map[string][]faceCandidate // by normalised name read, largest first (up to faceKeep)
}

// faceKeep is how many of a person's tiles are kept to choose from: the
// largest is often a camera-off tile (a profile picture on a plain
// background).
const faceKeep = 6

var paren = regexp.MustCompile(`\s*\([^)]*\)`)

// speakerName is the person a label or name read names: Tomoe's
// "Person 3 (Kai Moreno)" is Kai Moreno; a tile's "Kai Moreno (Guest)" too.
func speakerName(s string) string {
	s = strings.TrimSpace(s)
	if (strings.HasPrefix(s, "Person ") || strings.HasPrefix(s, "Speaker ")) && strings.Contains(s, "(") {
		if i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')'); i >= 0 && j > i {
			return normName(s[i+1 : j])
		}
	}
	return normName(paren.ReplaceAllString(s, ""))
}

// faces scans the sessions' looks once per change of the session folder
// (a new session, or one saved again).
func (e *Engine) faces() map[string][]faceCandidate {
	e.fi.mu.Lock()
	defer e.fi.mu.Unlock()
	dir := e.Cfg.TomoeSessions
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	h := sha1.New()
	for _, d := range ents {
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s %d|", d.Name(), info.ModTime().Unix())
		}
	}
	sig := hex.EncodeToString(h.Sum(nil))
	if sig == e.fi.sig && e.fi.best != nil {
		return e.fi.best
	}
	best := map[string][]faceCandidate{}
	for _, d := range ents {
		sd := filepath.Join(dir, d.Name())
		f, err := os.Open(filepath.Join(sd, "looks.jsonl"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<22)
		for sc.Scan() {
			var l struct {
				ID      int    `json:"id"`
				ThumbOf int    `json:"thumb_of"`
				Name    string `json:"name"`
				Width   float64
				Height  float64
				Shapes  []struct {
					Box  struct{ X, Y, Width, Height float64 }
					Ring bool `json:"ring"`
				} `json:"shapes"`
			}
			if json.Unmarshal(sc.Bytes(), &l) != nil || l.Name == "" || l.Width == 0 || l.Height == 0 {
				continue
			}
			var ring *struct{ X, Y, Width, Height float64 }
			rings := 0
			for i := range l.Shapes {
				if l.Shapes[i].Ring {
					rings++
					ring = &l.Shapes[i].Box
				}
			}
			if rings != 1 || ring.Width < 120 || ring.Height < 90 {
				continue // no single clear tile, or a thumbnail-sized one
			}
			id := l.ID
			if l.ThumbOf != 0 {
				id = l.ThumbOf
			}
			thumb := filepath.Join(sd, "looks", fmt.Sprintf("%d.jpg", id))
			n := speakerName(l.Name)
			area := ring.Width * ring.Height
			if cs := best[n]; n == "" || len(cs) == faceKeep && area <= cs[faceKeep-1].area {
				continue
			}
			if _, err := os.Stat(thumb); err != nil {
				continue
			}
			cs := append(best[n], faceCandidate{thumb: thumb, x: ring.X / l.Width, y: ring.Y / l.Height, w: ring.Width / l.Width, h: ring.Height / l.Height, area: area})
			sort.Slice(cs, func(a, b int) bool { return cs[a].area > cs[b].area })
			best[n] = cs[:min(len(cs), faceKeep)]
		}
		f.Close()
	}
	e.fi.sig, e.fi.best = sig, best
	return best
}

// headshot is the person's clearest meeting tile as a JPEG data URL, or
// "": a tile with the camera on when there is one (largest first),
// otherwise the profile picture in the middle of a camera-off tile.
func (e *Engine) headshot(p *person) string {
	if e.Cfg.TomoeSessions == "" {
		return ""
	}
	fs := e.faces()
	var cs []faceCandidate
	for n := range p.names {
		cs = append(cs, fs[n]...)
	}
	sort.Slice(cs, func(a, b int) bool { return cs[a].area > cs[b].area })
	var off *image.Image
	for _, c := range cs {
		img, err := crop(c)
		if err != nil {
			continue
		}
		if cameraOn(img) {
			return dataURL(img)
		}
		if off == nil {
			off = &img
		}
	}
	if off == nil {
		return ""
	}
	return dataURL(centre(*off, 0.5))
}

func dataURL(img image.Image) string {
	var buf bytes.Buffer
	if jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}) != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// cameraOn tells a live video tile from a camera-off one: the frame
// around a camera-off tile's picture is one plain colour (a flat or gently
// shaded background), while video varies there.
func cameraOn(img image.Image) bool {
	b := img.Bounds()
	var sum, sq, n float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			fx, fy := float64(x-b.Min.X)/float64(b.Dx()), float64(y-b.Min.Y)/float64(b.Dy())
			if fx > 0.2 && fx < 0.8 && fy > 0.15 && fy < 0.85 {
				continue // the middle, where a camera-off picture sits
			}
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
			sum, sq, n = sum+l, sq+l*l, n+1
		}
	}
	if n == 0 {
		return false
	}
	mean := sum / n
	return sq/n-mean*mean > 12*12 // standard deviation over 12 levels
}

// centre crops the middle square of img, share of its height on a side.
func centre(img image.Image, share float64) image.Image {
	b := img.Bounds()
	side := int(float64(b.Dy()) * share)
	cx, cy := b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2
	r := image.Rect(cx-side/2, cy-side/2, cx+side/2, cy+side/2).Intersect(b)
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok && !r.Empty() {
		return s.SubImage(r)
	}
	return img
}

func crop(c faceCandidate) (image.Image, error) {
	f, err := os.Open(c.thumb)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, err
	}
	bd := img.Bounds()
	r := image.Rect(bd.Min.X+int(c.x*float64(bd.Dx())), bd.Min.Y+int(c.y*float64(bd.Dy())),
		bd.Min.X+int((c.x+c.w)*float64(bd.Dx())), bd.Min.Y+int((c.y+c.h)*float64(bd.Dy()))).Intersect(bd)
	if r.Empty() {
		return nil, fmt.Errorf("empty crop")
	}
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, fmt.Errorf("can't crop %T", img)
	}
	return sub.SubImage(r), nil
}

// VoiceClip is a stretch of a Tomoe recording attributed to the person;
// the app plays it from /replay-audio/<Session> between Start and End.
type VoiceClip struct {
	Session string    `json:"session"`
	Start   float64   `json:"start"`
	End     float64   `json:"end"`
	Title   string    `json:"title"`
	At      time.Time `json:"at"`
	Text    string    `json:"text"`
}

// voiceClip picks, from the most recent recordings, the longest turn of
// the person's between 5 and 15 seconds (long enough to recognise, short
// enough to play at once), in a session whose audio is on disk.
func (e *Engine) voiceClip(p *person, items []item.Item) *VoiceClip {
	if e.Cfg.TomoeSessions == "" {
		return nil
	}
	var recs []*item.Item
	for i := range items {
		if items[i].Source == "tomoe" && items[i].Kind == item.KindMeeting {
			recs = append(recs, &items[i])
		}
	}
	sort.Slice(recs, func(a, b int) bool { return recs[a].OccurredAt.After(recs[b].OccurredAt) })
	for _, it := range recs {
		var best *item.Segment
		for k := range it.Segments {
			s := &it.Segments[k]
			d := s.End - s.Start
			if d < 5 || d > 15 || !p.names[speakerName(s.Speaker)] {
				continue
			}
			if best == nil || d > best.End-best.Start {
				best = s
			}
		}
		if best == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.Cfg.TomoeSessions, filepath.Base(it.Ref), "audio.m4a")); err != nil {
			continue
		}
		title := it.Title
		if t, err := e.Store.ItemTitles(sensitivity.Owner(), []string{it.ID}); err == nil && t[it.ID].Title != "" {
			title = t[it.ID].Title // the meeting's name, not Tomoe's default
		}
		return &VoiceClip{Session: filepath.Base(it.Ref), Start: best.Start, End: best.End, Title: title, At: it.OccurredAt, Text: best.Text}
	}
	return nil
}
