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
	// direct: the name was read under the highlight in this look (not
	// carried over from an earlier read of the tile's position).
	direct bool
}

type faceIndex struct {
	mu   sync.Mutex
	sig  string
	best map[string][]faceCandidate // by normalised name read, direct reads then largest first (up to faceKeep)
	// shots caches chosen headshots by person, for this sig.
	shots map[string]string
}

// faceKeep bounds how many of a person's tiles the index keeps; faceSample
// is how many of them face detection looks at, spread evenly across tile
// sizes. Taking only the largest tiles fails: those are full-screen views,
// often of a camera-off profile picture, while live video sits in smaller
// gallery tiles.
var faceKeep, faceSample = 4000, 60

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
				Window  string `json:"window"`
				Detail  string `json:"detail"`
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
			// In a one-to-one call the name is the window title, whoever is
			// highlighted (often the owner): not evidence of whose tile it is.
			if win := l.Window; win != "" {
				if i := strings.Index(win, " — "); i >= 0 {
					win = win[i+len(" — "):]
				}
				if speakerName(win) == speakerName(l.Name) {
					continue
				}
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
			if n == "" {
				continue
			}
			cs := append(best[n], faceCandidate{thumb: thumb, x: ring.X / l.Width, y: ring.Y / l.Height, w: ring.Width / l.Width, h: ring.Height / l.Height, area: area,
				direct: strings.HasPrefix(l.Detail, "read ")})
			best[n] = cs
		}
		f.Close()
	}
	for n, cs := range best {
		sortCandidates(cs)
		best[n] = cs[:min(len(cs), faceKeep)]
	}
	e.fi.sig, e.fi.best, e.fi.shots = sig, best, map[string]string{}
	return best
}

// sample takes n candidates spread evenly across the list (direct reads
// only when there are enough of them), keeping the order.
func sample(cs []faceCandidate, n int) []faceCandidate {
	var direct []faceCandidate
	for _, c := range cs {
		if c.direct {
			direct = append(direct, c)
		}
	}
	if len(direct) >= n/2 {
		cs = direct
	}
	if len(cs) <= n {
		return cs
	}
	out := make([]faceCandidate, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, cs[i*len(cs)/n])
	}
	return out
}

// sortCandidates puts direct reads first, then larger tiles.
func sortCandidates(cs []faceCandidate) {
	sort.SliceStable(cs, func(a, b int) bool {
		if cs[a].direct != cs[b].direct {
			return cs[a].direct
		}
		return cs[a].area > cs[b].area
	})
}

// headshot is the person's clearest meeting tile as a JPEG data URL, or
// "": a tile with the camera on when there is one (largest first),
// otherwise the profile picture in the middle of a camera-off tile.
func (e *Engine) headshot(p *person) string {
	if e.Cfg.TomoeSessions == "" {
		return ""
	}
	e.fi.mu.Lock()
	cached, ok := e.fi.shots[p.id]
	e.fi.mu.Unlock()
	if ok {
		return cached
	}
	// Choosing takes a few seconds (face detection over a sample of tiles),
	// so a choice is kept on disk for a week.
	h := sha1.Sum([]byte(p.id))
	disk := filepath.Join(e.Cfg.DataDir, "headshots", hex.EncodeToString(h[:10])+".txt")
	if st, err := os.Stat(disk); err == nil && time.Since(st.ModTime()) < 7*24*time.Hour {
		if b, err := os.ReadFile(disk); err == nil {
			e.fi.mu.Lock()
			if e.fi.shots != nil {
				e.fi.shots[p.id] = string(b)
			}
			e.fi.mu.Unlock()
			return string(b)
		}
	}
	fs := e.faces()
	var cs []faceCandidate
	for n := range p.names {
		cs = append(cs, fs[n]...)
	}
	sortCandidates(cs)
	shot := ""
	if haveVision {
		shot = faceHeadshot(sample(cs, faceSample))
	}
	if shot == "" {
		shot = tileHeadshot(cs)
	}
	e.fi.mu.Lock()
	if e.fi.shots != nil {
		e.fi.shots[p.id] = shot
	}
	e.fi.mu.Unlock()
	if e.Cfg.DataDir != "" && os.MkdirAll(filepath.Dir(disk), 0o700) == nil {
		_ = os.WriteFile(disk, []byte(shot), 0o600)
	}
	return shot
}

// tileHeadshot is the fallback without face detection: a camera-on tile,
// else the middle of a camera-off one.
func tileHeadshot(cs []faceCandidate) string {
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

// faceShot is one candidate's face, as Vision saw it.
type faceShot struct {
	img     image.Image // the face, framed with some room around it
	size    float64     // face height as a share of the tile
	quality float64
	print   []float32
}

// faceHeadshot picks the person's face among their tiles with macOS
// Vision: it finds the faces, compares them (feature prints), and takes
// the face that recurs most (so a slide, or someone else shown under the
// name, loses), preferring live video (a large face) to a profile
// picture (a small one in a circle), then the best capture quality. ""
// when no tile has a face.
func faceHeadshot(cs []faceCandidate) string {
	var shots []faceShot
	for _, c := range cs {
		tile, err := crop(c)
		if err != nil {
			continue
		}
		var best *visionFace
		for _, f := range detectFaces(jpegBytes(tile)) {
			if best == nil || f.h > best.h {
				f := f
				best = &f
			}
		}
		if best == nil || best.quality < 0.1 {
			continue
		}
		face := frame(tile, *best, 1.9)
		fp := featurePrint(jpegBytes(face))
		if fp == nil {
			continue
		}
		shots = append(shots, faceShot{img: face, size: best.h, quality: best.quality, print: fp})
	}
	if len(shots) == 0 {
		return ""
	}
	var live []faceShot
	for _, s := range shots {
		if s.size >= 0.28 {
			live = append(live, s)
		}
	}
	pool := shots
	if len(live) >= 2 {
		pool = live
	}
	// The medoid: the face closest, on average, to the others.
	m, bestMean := 0, -1.0
	for i := range pool {
		sum := 0.0
		for j := range pool {
			if i != j {
				sum += dist(pool[i].print, pool[j].print)
			}
		}
		if mean := sum / float64(max(1, len(pool)-1)); bestMean < 0 || mean < bestMean {
			m, bestMean = i, mean
		}
	}
	// Among the faces like the medoid (no farther than the median distance
	// to it), the clearest.
	ds := make([]float64, len(pool))
	for i := range pool {
		ds[i] = dist(pool[i].print, pool[m].print)
	}
	sorted := append([]float64(nil), ds...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	score := func(s faceShot) float64 { return s.quality * min(1, s.size/0.4) }
	pick := m
	for i := range pool {
		if ds[i] <= median && score(pool[i]) > score(pool[pick]) {
			pick = i
		}
	}
	return dataURL(pool[pick].img)
}

func dist(a, b []float32) float64 {
	n := min(len(a), len(b))
	s := 0.0
	for i := 0; i < n; i++ {
		d := float64(a[i] - b[i])
		s += d * d
	}
	return s
}

// frame crops a square around a face, room times its size, within img.
func frame(img image.Image, f visionFace, room float64) image.Image {
	b := img.Bounds()
	cx := b.Min.X + int((f.x+f.w/2)*float64(b.Dx()))
	cy := b.Min.Y + int((f.y+f.h/2)*float64(b.Dy()))
	side := int(room * max(f.w*float64(b.Dx()), f.h*float64(b.Dy())))
	r := image.Rect(cx-side/2, cy-side/2, cx+side/2, cy+side/2).Intersect(b)
	if s, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok && !r.Empty() {
		return s.SubImage(r)
	}
	return img
}

func jpegBytes(img image.Image) []byte {
	var buf bytes.Buffer
	if jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}) != nil {
		return nil
	}
	return buf.Bytes()
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
