package phone

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/recorder/control"
)

// A phone's recording is fetched as numbered ADTS chunks, each checked
// against its SHA-256 and size in the phone's meta.json. Chunks are staged
// in the Musubi data folder (phone/recordings/<rid>/) until the recorder
// has the meeting saved, so an interrupted sync resumes where it stopped.

const (
	maxChunkBytes = 64 << 20
	maxChunks     = 10_000
	// maxRecordingBytes bounds what one recording may stage: 2 GB, about
	// 46 hours at the phone's 96 kbps.
	maxRecordingBytes = 2 << 30
	stateFile         = "state.json"
)

// StateReceived: every chunk is on the Mac, not yet with the recorder.
// The other states are the recorder's import states (control.Import*).
const StateReceived = "received"

var (
	ridPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	nPattern   = regexp.MustCompile(`^[0-9]{1,4}$`)
	shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// parseRID accepts a UUID in canonical form (any case), returning it in
// lower case: the only shape that becomes a folder name.
func parseRID(s string) (string, bool) {
	s = strings.ToLower(s)
	return s, ridPattern.MatchString(s)
}

// parseN accepts a chunk number, 0 to 9999, digits only.
func parseN(s string) (int, bool) {
	if !nPattern.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n < maxChunks
}

func chunkName(n int) string { return fmt.Sprintf("chunk-%04d.aac", n) }

// recState is a recording's state.json.
type recState struct {
	// Phone is the certificate SHA-256 of the phone it came from; a
	// recording is only ever another phone's by mistake or malice.
	Phone string `json:"phone,omitempty"`
	// State is "" while chunks are being fetched.
	State string `json:"state"`
	// Retry: a failure on the Mac's side (damaged chunk, join, handing
	// over), tried again at the next sync; a failed import is not.
	Retry     bool      `json:"retry,omitempty"`
	Session   string    `json:"session_id,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	EventID   string    `json:"event_id,omitempty"`
	Inbox     string    `json:"inbox,omitempty"`
	// DetailsRev is the rev of the phone's details last written into the
	// session folder.
	DetailsRev int64     `json:"details_rev,omitempty"`
	Updated    time.Time `json:"updated"`
}

func readState(dir string) (*recState, error) {
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return nil, err
	}
	var s recState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func writeState(dir string, s *recState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, stateFile), b)
}

// meta is the phone's meta.json (android RecordingMeta.toJson).
type meta struct {
	Version    int     `json:"version"`
	ID         string  `json:"id"`
	Start      string  `json:"start"`
	End        *string `json:"end"`
	DeviceName string  `json:"device_name"`
	Event      *struct {
		ID      string `json:"id"`
		Subject string `json:"subject"`
		Start   string `json:"start"`
	} `json:"event"`
	Chunks []struct {
		N      int    `json:"n"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"chunks"`
}

// metaChunk is one chunk meta.json lists, checked.
type metaChunk struct {
	n    int
	size int64
	sum  string
}

// parseMeta checks a meta.json (for rid, or any recording when rid is
// ""): its id, a start time with zone,
// an event id of the right shape, and a non-empty list of distinct
// chunks in range. It returns the chunks in order.
func parseMeta(b []byte, rid string) (*meta, time.Time, []metaChunk, error) {
	var m meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, time.Time{}, nil, fmt.Errorf("meta.json isn't JSON")
	}
	if id, ok := parseRID(m.ID); !ok || (rid != "" && id != rid) {
		return nil, time.Time{}, nil, fmt.Errorf("meta.json is for another recording")
	}
	start, err := time.Parse(time.RFC3339Nano, m.Start)
	if err != nil {
		return nil, time.Time{}, nil, fmt.Errorf("meta.json start isn't a time with a zone")
	}
	if m.Event != nil && m.Event.ID != "" && !control.ValidEventID(m.Event.ID) {
		return nil, time.Time{}, nil, fmt.Errorf("meta.json event id isn't one")
	}
	if len(m.Chunks) == 0 || len(m.Chunks) > maxChunks {
		return nil, time.Time{}, nil, fmt.Errorf("meta.json lists no chunks")
	}
	seen := map[int]bool{}
	var out []metaChunk
	var total int64
	for _, c := range m.Chunks {
		sum := strings.ToLower(c.SHA256)
		if c.N < 0 || c.N >= maxChunks || seen[c.N] || c.Size < 0 || c.Size > maxChunkBytes || !shaPattern.MatchString(sum) {
			return nil, time.Time{}, nil, fmt.Errorf("meta.json has a bad chunk entry")
		}
		if total += c.Size; total > maxRecordingBytes {
			return nil, time.Time{}, nil, fmt.Errorf("meta.json lists too much audio")
		}
		seen[c.N] = true
		out = append(out, metaChunk{c.N, c.Size, sum})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	m.ID = strings.ToLower(m.ID)
	return &m, start, out, nil
}

// fileSHA256 is a file's SHA-256 (hex) and size.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// verifyChunks checks every listed chunk against its size and hash,
// deleting any that doesn't match so the phone sends it again. bad lists
// the chunks to send (missing or damaged).
func verifyChunks(dir string, chunks []metaChunk) (bad []int, err error) {
	for _, c := range chunks {
		path := filepath.Join(dir, chunkName(c.n))
		sum, size, err := fileSHA256(path)
		if errors.Is(err, fs.ErrNotExist) {
			bad = append(bad, c.n)
			continue
		}
		if err != nil {
			return nil, err
		}
		if sum != c.sum || size != c.size {
			os.Remove(path)
			bad = append(bad, c.n)
		}
	}
	return bad, nil
}

// errNotADTS: a chunk holds no ADTS frame at its start.
var errNotADTS = errors.New("not ADTS audio")

// copyADTS copies r's whole ADTS frames to w, checking each header; a
// last frame cut short (the phone died mid-write) is dropped, as a reader
// would. It returns how many frames it copied.
func copyADTS(w io.Writer, r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var hdr [7]byte
	frames := 0
	buf := make([]byte, 8192)
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return frames, err
		}
		// Sync word 0xFFF, layer 0; frame length (13 bits) includes the
		// header.
		if hdr[0] != 0xFF || hdr[1]&0xF6 != 0xF0 {
			if frames == 0 {
				return 0, errNotADTS
			}
			return frames, fmt.Errorf("%w after frame %d", errNotADTS, frames)
		}
		length := int(hdr[3]&0x03)<<11 | int(hdr[4])<<3 | int(hdr[5])>>5
		if length < 7 {
			return frames, fmt.Errorf("%w: bad frame length", errNotADTS)
		}
		if cap(buf) < length-7 {
			buf = make([]byte, length)
		}
		body := buf[:length-7]
		if _, err := io.ReadFull(br, body); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break // a frame cut short at the end
			}
			return frames, err
		}
		if _, err := w.Write(hdr[:]); err != nil {
			return frames, err
		}
		if _, err := w.Write(body); err != nil {
			return frames, err
		}
		frames++
	}
	return frames, nil
}

// joinChunks writes the chunks' frames, in order, into dst (mode 0600)
// through a temporary file in its folder.
func joinChunks(dir string, chunks []metaChunk, dst string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".phone-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return 0, err
	}
	w := bufio.NewWriterSize(tmp, 256<<10)
	total := 0
	for _, c := range chunks {
		f, err := os.Open(filepath.Join(dir, chunkName(c.n)))
		if err != nil {
			tmp.Close()
			return 0, err
		}
		n, err := copyADTS(w, f)
		f.Close()
		total += n
		if err != nil {
			tmp.Close()
			return 0, fmt.Errorf("chunk %d: %w", c.n, err)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, errNotADTS
	}
	return total, os.Rename(tmp.Name(), dst)
}

// dropChunks deletes a recording's audio once the recorder has it.
func dropChunks(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "chunk-") || strings.HasPrefix(e.Name(), ".chunk-") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
