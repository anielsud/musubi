package phone

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/anielsud/musubi/internal/phonedetails"
	"github.com/anielsud/musubi/internal/recorder/control"
)

// Recording details (docs/phone.md, "Recording details"): the notes,
// people and photos the owner adds to a recording on the phone. Once the
// recorder has the meeting saved, the Mac fetches the listed photos (op
// `file`, each checked against its size and SHA-256, staged in the
// recording's staging folder so an interrupted sync resumes), writes them
// and phone-details.json into the session folder, replacing what an
// earlier rev wrote, and reports the rev it applied (`details_rev`).

// metaDetails is meta.json's details, checked; nil when the meta has
// none (version 1) or they don't hold up.
func metaDetails(raw json.RawMessage) (*phonedetails.MetaDetails, error) {
	var m struct {
		Details *phonedetails.MetaDetails `json:"details"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Details == nil {
		return nil, nil
	}
	if err := m.Details.Check(); err != nil {
		return nil, err
	}
	return m.Details, nil
}

// sessionDirOf is the recorder's folder of session sid; "" unless sid is
// a single folder name and that folder is there (not a link).
func (s *Syncer) sessionDirOf(sid string) string {
	if sid == "" || sid == "." || sid == ".." || filepath.Base(sid) != sid || strings.ContainsAny(sid, `/\`) {
		return ""
	}
	dir := filepath.Join(s.cfg.RecorderData, "sessions", sid)
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// applyDetails brings the session folder up to d for an imported
// recording. It returns whether d is now applied; only a
// broken connection is an error. The notes' text is fetched (notes.txt)
// unless the session folder already has it. A photo the phone no longer
// has (it deletes them once the Mac has them) is kept from the session
// folder.
func (s *Syncer) applyDetails(fc *frameConn, rid string, st *recState, d *phonedetails.MetaDetails) (bool, error) {
	if st.State != control.ImportImported {
		return false, nil
	}
	if d.Rev == st.DetailsRev {
		return true, nil
	}
	sess := s.sessionDirOf(st.Session)
	if sess == "" {
		s.cfg.Logf("phone: recording %s: session folder not found; details wait", rid)
		return false, nil
	}
	stage := filepath.Join(s.recDir(rid), "details")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return false, err
	}
	notes, ok, err := s.notesText(fc, rid, stage, sess, d.Notes)
	if err != nil || !ok {
		return false, err
	}
	var kept []phonedetails.Photo
	for _, p := range d.Photos {
		got, err := s.stageFile(fc, rid, stage, p.Name, p.Size, p.SHA256, phonedetails.PhotoPath(sess, p.Name))
		if err != nil {
			return false, err
		}
		switch got {
		case photoStaged:
			kept = append(kept, p)
		case photoGone:
			s.cfg.Logf("phone: recording %s: photo %s is no longer on the phone; left out", rid, p.Name)
		case photoDamaged:
			// Fetched again at the next sync, with the rest.
			s.cfg.Logf("phone: recording %s: photo %s arrived damaged; details wait", rid, p.Name)
			return false, nil
		}
	}
	if err := replacePhotos(sess, stage, kept); err != nil {
		s.cfg.Logf("phone: recording %s: photos not written: %v", rid, err)
		return false, nil
	}
	out := phonedetails.File{Writer: phonedetails.Writer, Recording: rid, Details: phonedetails.Details{
		Rev: d.Rev, Attendees: d.Attendees, Notes: phonedetails.Notes{Text: notes, Updated: d.Notes.Updated}, Photos: kept}}
	if out.Attendees == nil {
		out.Attendees = []phonedetails.Attendee{}
	}
	if out.Photos == nil {
		out.Photos = []phonedetails.Photo{}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err == nil {
		err = writePrivate(filepath.Join(sess, phonedetails.FileName), b)
	}
	if err != nil {
		s.cfg.Logf("phone: recording %s: details not written: %v", rid, err)
		return false, nil
	}
	os.RemoveAll(stage) // the session folder has them now
	st.DetailsRev = d.Rev
	s.save(rid, st)
	// Counts only: the notes and names stay out of the log.
	s.cfg.Logf("phone: recording %s: details rev %d applied (%d people, %d photos, notes %t)", rid, d.Rev, len(d.Attendees), len(kept), notes != "")
	if s.cfg.OnDetails != nil {
		s.cfg.OnDetails()
	}
	return true, nil
}

type photoGot int

const (
	photoStaged  photoGot = iota
	photoGone             // the phone doesn't have it any more
	photoDamaged          // it arrived other than listed
)

// notesText is the notes' text for ref: none, the text the session
// folder already has (same hash), or notes.txt fetched and checked. ok
// is false when it didn't arrive right (the details wait for the next
// sync).
func (s *Syncer) notesText(fc *frameConn, rid, stage, sess string, ref phonedetails.NotesRef) (string, bool, error) {
	if ref.Size == 0 {
		return "", true, nil
	}
	if f := phonedetails.Read(sess); f != nil {
		if sum := sha256.Sum256([]byte(f.Notes.Text)); hex.EncodeToString(sum[:]) == ref.SHA256 && int64(len(f.Notes.Text)) == ref.Size {
			return f.Notes.Text, true, nil
		}
	}
	got, err := s.stageFile(fc, rid, stage, phonedetails.NotesFile, ref.Size, ref.SHA256, "")
	if err != nil || got != photoStaged {
		if err == nil {
			s.cfg.Logf("phone: recording %s: notes didn't arrive; details wait", rid)
		}
		return "", false, err
	}
	b, err := os.ReadFile(filepath.Join(stage, phonedetails.NotesFile))
	if err == nil {
		err = phonedetails.CheckNotes(b)
	}
	if err != nil {
		s.cfg.Logf("phone: recording %s: notes not usable: %v", rid, err)
		os.Remove(filepath.Join(stage, phonedetails.NotesFile))
		return "", false, nil
	}
	return string(b), true, nil
}

// stageFile makes sure stage holds the details file name (size bytes,
// SHA-256 sum), checked: already staged, copied from have (an earlier
// rev's copy in the session folder, "" for none), or fetched.
func (s *Syncer) stageFile(fc *frameConn, rid, stage, name string, wantSize int64, wantSum, have string) (photoGot, error) {
	p := struct {
		Name   string
		Size   int64
		SHA256 string
	}{name, wantSize, wantSum}
	path := filepath.Join(stage, p.Name)
	if sum, size, err := fileSHA256(path); err == nil && sum == p.SHA256 && size == p.Size {
		return photoStaged, nil
	}
	os.Remove(path)
	if src := have; src != "" {
		if fi, err := os.Lstat(src); err == nil && fi.Mode().IsRegular() {
			if sum, size, err := fileSHA256(src); err == nil && sum == p.SHA256 && size == p.Size {
				return photoStaged, copyPrivate(src, path)
			}
		}
	}
	if err := fc.send(map[string]any{"op": "file", "rid": rid, "name": p.Name}, nil, 0); err != nil {
		return photoDamaged, err
	}
	var rep struct {
		SHA256 string `json:"sha256"`
	}
	n, err := fc.recv(frameTimeout, &rep, true)
	var pe *phoneError
	if errors.As(err, &pe) {
		if pe.code == "gone" || pe.code == "unknown_file" {
			return photoGone, nil
		}
		return photoDamaged, nil
	}
	if err != nil {
		return photoDamaged, err
	}
	if n != p.Size {
		// Read the body off the connection either way; it is not kept.
		if err := fc.readBody(io.Discard); err != nil {
			return photoDamaged, err
		}
		return photoDamaged, nil
	}
	tmp, err := os.CreateTemp(stage, ".photo-*")
	if err != nil {
		return photoDamaged, err
	}
	h := sha256.New()
	err = fc.readBody(io.MultiWriter(tmp, h))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return photoDamaged, err
	}
	if strings.ToLower(rep.SHA256) != p.SHA256 || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		os.Remove(tmp.Name())
		return photoDamaged, nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return photoDamaged, err
	}
	return photoStaged, nil
}

// replacePhotos puts exactly photos (from stage) into the session's
// photos folder: written to a new folder first, then swapped in.
func replacePhotos(sess, stage string, photos []phonedetails.Photo) error {
	tmp, err := os.MkdirTemp(sess, ".photos-new-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.Chmod(tmp, 0o700); err != nil {
		return err
	}
	for _, p := range photos {
		if err := copyPrivate(filepath.Join(stage, p.Name), filepath.Join(tmp, p.Name)); err != nil {
			return err
		}
	}
	dst := filepath.Join(sess, phonedetails.PhotosDir)
	old := ""
	if fi, err := os.Lstat(dst); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s is not a folder", phonedetails.PhotosDir)
		}
		old = tmp + "-old"
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(photos) == 0 {
		if old != "" {
			os.RemoveAll(old)
		}
		return nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		if old != "" {
			os.Rename(old, dst)
		}
		return err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	return nil
}

// copyPrivate copies a file to dst (mode 0600) through a temporary file.
func copyPrivate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, io.LimitReader(in, phonedetails.MaxPhotoBytes+1)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
