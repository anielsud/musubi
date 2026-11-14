// Package phonedetails is a phone recording's details (docs/phone.md,
// "Recording details"): the owner's notes, the people present and photos,
// as the phone's meta.json carries them and as phone sync writes them
// into the recorder's session folder (phone-details.json, photos/). It
// has no dependencies, so phone sync, ingest, the recorder and the app
// read the one shape with the same bounds.
package phonedetails

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// FileName is the details in a session folder; PhotosDir the photos.
	FileName  = "phone-details.json"
	PhotosDir = "photos"
	// Writer marks the file as phone sync's: no other writer is trusted.
	Writer = "musubi"

	MaxNotes = 20_000 // characters
	// MaxNotesBytes bounds notes.txt: MaxNotes characters of at most 4
	// bytes each.
	MaxNotesBytes = MaxNotes * 4
	// NotesFile is the notes' file on the phone, fetched with `file`.
	NotesFile     = "notes.txt"
	MaxAttendees  = 50
	MaxName       = 200 // characters
	MaxEmail      = 320
	MaxPhotos     = 30
	MaxPhotoBytes = 15 << 20
	// MaxFileBytes bounds phone-details.json: 20,000 characters of notes
	// escaped at worst, plus the lists.
	MaxFileBytes = 1 << 20
)

// photoName is the only shape a photo's file name has: it can never name
// a path outside the photos folder.
var photoName = regexp.MustCompile(`^photo-[0-9]{4}\.jpg$`)

var shaHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidPhotoName reports whether name is a photo's file name.
func ValidPhotoName(name string) bool { return photoName.MatchString(name) }

type Attendee struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type Notes struct {
	Text    string `json:"text"`
	Updated string `json:"updated,omitempty"`
}

type Photo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Taken  string `json:"taken,omitempty"`
}

// NotesRef is meta.json's notes: the text is NotesFile, fetched like a
// photo, so a long note never makes meta.json (and the phone's list)
// large.
type NotesRef struct {
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Updated string `json:"updated,omitempty"`
}

// MetaDetails is meta.json's "details".
type MetaDetails struct {
	Rev       int64      `json:"rev"`
	Attendees []Attendee `json:"attendees"`
	Notes     NotesRef   `json:"notes"`
	Photos    []Photo    `json:"photos"`
}

// Check validates m in place, as Details.Check does, with the notes'
// size and hash in place of their text.
func (m *MetaDetails) Check() error {
	d := Details{Rev: m.Rev, Attendees: m.Attendees, Notes: Notes{Updated: m.Notes.Updated}, Photos: m.Photos}
	if err := d.Check(); err != nil {
		return err
	}
	m.Notes.SHA256 = strings.ToLower(m.Notes.SHA256)
	if m.Notes.Size < 0 || m.Notes.Size > MaxNotesBytes || (m.Notes.Size > 0 && !shaHex.MatchString(m.Notes.SHA256)) {
		return errors.New("details: notes are not listed right")
	}
	m.Attendees, m.Photos = d.Attendees, d.Photos
	return nil
}

// CheckNotes checks notes.txt's text: UTF-8, at most MaxNotes characters.
func CheckNotes(b []byte) error {
	if !utf8.Valid(b) || utf8.RuneCount(b) > MaxNotes {
		return errors.New("details: notes aren't UTF-8 text of at most 20,000 characters")
	}
	return nil
}

// Details is the details as phone-details.json keeps them, the notes'
// text in place.
type Details struct {
	Rev       int64      `json:"rev"`
	Attendees []Attendee `json:"attendees"`
	Notes     Notes      `json:"notes"`
	Photos    []Photo    `json:"photos"`
}

// File is phone-details.json: the details and which recording they came
// from.
type File struct {
	Writer    string `json:"writer"`
	Recording string `json:"recording"`
	Details
}

// Empty reports whether there is nothing to show.
func (d *Details) Empty() bool {
	return d == nil || (strings.TrimSpace(d.Notes.Text) == "" && len(d.Attendees) == 0 && len(d.Photos) == 0)
}

// Check validates d in place: a rev, at most MaxAttendees printable
// names, notes of at most MaxNotes characters, at most MaxPhotos photos
// of the photo name shape, a size up to MaxPhotoBytes and a SHA-256. Any
// violation rejects the whole details (they come from another device).
func (d *Details) Check() error {
	if d.Rev < 0 {
		return errors.New("details: negative rev")
	}
	if len(d.Attendees) > MaxAttendees {
		return errors.New("details: too many people")
	}
	for i, a := range d.Attendees {
		name := strings.TrimSpace(a.Name)
		if name == "" || utf8.RuneCountInString(name) > MaxName || !printable(name) {
			return fmt.Errorf("details: person %d has no usable name", i)
		}
		email := strings.TrimSpace(a.Email)
		if len(email) > MaxEmail || !printable(email) || (email != "" && !strings.Contains(email, "@")) {
			return fmt.Errorf("details: person %d has a bad email", i)
		}
		d.Attendees[i] = Attendee{Name: name, Email: strings.ToLower(email)}
	}
	if !utf8.ValidString(d.Notes.Text) || utf8.RuneCountInString(d.Notes.Text) > MaxNotes {
		return errors.New("details: notes too long")
	}
	if d.Notes.Updated != "" {
		if _, err := time.Parse(time.RFC3339Nano, d.Notes.Updated); err != nil {
			return errors.New("details: notes time isn't one")
		}
	}
	if len(d.Photos) > MaxPhotos {
		return errors.New("details: too many photos")
	}
	seen := map[string]bool{}
	for i, p := range d.Photos {
		p.SHA256 = strings.ToLower(p.SHA256)
		if !ValidPhotoName(p.Name) || seen[p.Name] || p.Size <= 0 || p.Size > MaxPhotoBytes || !shaHex.MatchString(p.SHA256) {
			return fmt.Errorf("details: photo %d is not one", i)
		}
		if p.Taken != "" {
			if _, err := time.Parse(time.RFC3339Nano, p.Taken); err != nil {
				return fmt.Errorf("details: photo %d time isn't one", i)
			}
		}
		seen[p.Name] = true
		d.Photos[i] = p
	}
	return nil
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// Parse reads and checks phone-details.json's bytes; only phone sync's
// file is taken.
func Parse(b []byte) (*File, error) {
	if len(b) > MaxFileBytes {
		return nil, errors.New("details: file too large")
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, errors.New("details: not JSON")
	}
	if f.Writer != Writer {
		return nil, errors.New("details: not phone sync's")
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	return &f, nil
}

// ReadRaw reads a session folder's phone-details.json, bounded, refusing
// anything but a regular file; nil, nil when there is none.
func ReadRaw(sessionDir string) ([]byte, error) {
	path := filepath.Join(sessionDir, FileName)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxFileBytes {
		return nil, errors.New("details: not a regular file of a sane size")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFileBytes {
		return nil, errors.New("details: file too large")
	}
	return b, nil
}

// Read is a session folder's details, or nil when it has none (or none
// phone sync wrote that hold up).
func Read(sessionDir string) *File {
	b, err := ReadRaw(sessionDir)
	if err != nil || b == nil {
		return nil
	}
	f, err := Parse(b)
	if err != nil {
		return nil
	}
	return f
}

// PhotoPath is a listed photo's file in a session folder; "" for a name
// that isn't a photo's.
func PhotoPath(sessionDir, name string) string {
	if !ValidPhotoName(name) {
		return ""
	}
	return filepath.Join(sessionDir, PhotosDir, name)
}
