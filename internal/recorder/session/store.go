package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionFile is the name of the transcript file inside a session's
// directory. Tomoe uses the same layout, <baseDir>/<id>/session.json, so
// either app can open the other's recordings.
const sessionFile = "session.json"

// Store keeps sessions on disk, one directory per session under baseDir.
type Store struct {
	baseDir string
}

// NewStore returns a Store for sessions kept under baseDir.
func NewStore(baseDir string) *Store {
	return &Store{baseDir: baseDir}
}

func (s *Store) dir(id string) string { return filepath.Join(s.baseDir, id) }

// Save writes sess to <baseDir>/<id>/session.json, creating the directory
// if needed and replacing any earlier copy.
func (s *Store) Save(sess *Session) error {
	dir := s.dir(sess.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("making session directory: %w", err)
	}
	// Indented, as Tomoe writes it, so the files stay readable and diff
	// cleanly between the two apps.
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding session: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionFile), data, 0o644); err != nil {
		return fmt.Errorf("saving session file: %w", err)
	}
	return nil
}

// Load reads the session with the given ID. A shortened ID works as long
// as exactly one session starts with it, so a command line can take the
// first block of a UUID.
func (s *Store) Load(id string) (*Session, error) {
	full, err := s.lookup(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir(full), sessionFile))
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	sess := new(Session)
	if err := json.Unmarshal(data, sess); err != nil {
		return nil, fmt.Errorf("decoding session file: %w", err)
	}
	return sess, nil
}

// lookup turns a full or shortened ID into a session directory's name.
// A directory holding session.json under exactly that name wins;
// otherwise the ID must be the start of exactly one directory's name.
func (s *Store) lookup(id string) (string, error) {
	if _, err := os.Stat(filepath.Join(s.dir(id), sessionFile)); err == nil {
		return id, nil
	}
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return "", fmt.Errorf("listing sessions: %w", err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), id) {
			found = append(found, e.Name())
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no session with ID %q", id)
	}
	return "", fmt.Errorf("session ID %q is ambiguous: %d sessions start with it", id, len(found))
}

// List loads every session under baseDir, newest first. A missing baseDir
// just means nothing has been recorded yet. A directory that doesn't load
// is left out rather than failing the whole list, so one damaged session
// can't hide the others.
func (s *Store) List() ([]*Session, error) {
	entries, err := os.ReadDir(s.baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	var out []*Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if sess, err := s.Load(e.Name()); err == nil {
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].CreatedAt.After(out[b].CreatedAt) })
	return out, nil
}

// Delete removes a session's whole directory, audio included. IDs are
// matched as in Load.
func (s *Store) Delete(id string) error {
	full, err := s.lookup(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(s.dir(full)); err != nil {
		return fmt.Errorf("removing session: %w", err)
	}
	return nil
}
