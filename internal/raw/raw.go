// Package raw keeps every input exactly as it arrived, addressed by its
// SHA-256. Nothing here is ever edited; derived data is rebuilt from it.
package raw

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

type Store struct{ dir string }

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Store) path(hash string) string {
	return filepath.Join(s.dir, hash[:2], hash[2:])
}

// Put stores b and returns its hash. Storing the same bytes twice is a no-op.
func (s *Store) Put(b []byte) (string, error) {
	h := Hash(b)
	p := s.path(h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	return h, os.Rename(tmp, p)
}

func (s *Store) Get(hash string) ([]byte, error) { return os.ReadFile(s.path(hash)) }

// Delete removes a blob; used only by forget.
func (s *Store) Delete(hash string) error {
	err := os.Remove(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
