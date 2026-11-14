package phone

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// A paired phone is its pinned certificate SHA-256, with what Settings
// shows: its name, its last reported addresses (dialed when it isn't seen
// on the local network), when it paired and last synced, and how the last
// sync went. Kept in phones.json (mode 0600). There are no tokens.

const (
	pinsFile      = "phones.json"
	maxDeviceName = 64
	maxOutcome    = 200
)

// Device is a paired phone. ID is its certificate's SHA-256 (hex), which
// is not a secret: the phone advertises its first 16 digits.
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Paired      time.Time `json:"paired"`
	LastSync    time.Time `json:"last_sync,omitzero"`
	LastOutcome string    `json:"last_outcome,omitempty"`
	Addresses   []string  `json:"addresses"`
}

type pins struct {
	mu   sync.Mutex
	path string
	list []Device
}

func openPins(dir string) (*pins, error) {
	p := &pins{path: filepath.Join(dir, pinsFile)}
	b, err := os.ReadFile(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Phones []Device `json:"phones"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("paired phones: %w", err)
	}
	for _, d := range f.Phones {
		if shaPattern.MatchString(d.ID) {
			p.list = append(p.list, d)
		}
	}
	return p, nil
}

func (p *pins) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	list := p.list
	if list == nil {
		list = []Device{}
	}
	b, err := json.MarshalIndent(struct {
		Phones []Device `json:"phones"`
	}{list}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(p.path, b)
}

func copyDevice(d Device) Device {
	d.Addresses = append([]string{}, d.Addresses...)
	return d
}

// List is the paired phones.
func (p *pins) List() []Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Device, len(p.list))
	for i, d := range p.list {
		out[i] = copyDevice(d)
	}
	return out
}

// byPrefix is the paired phone whose fingerprint starts with fp16 (an
// advertisement's fp).
func (p *pins) byPrefix(fp16 string) (Device, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.list {
		if strings.HasPrefix(d.ID, fp16) {
			return copyDevice(d), true
		}
	}
	return Device{}, false
}

// get is the paired phone with this full fingerprint, compared in
// constant time (the comparison guards the connection).
func (p *pins) get(fp string) (Device, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := -1
	for i, d := range p.list {
		if len(d.ID) == len(fp) && subtle.ConstantTimeCompare([]byte(d.ID), []byte(fp)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return Device{}, false
	}
	return copyDevice(p.list[found]), true
}

// pin adds a phone, or renews one paired again.
func (p *pins) pin(d Device) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	prev := p.list
	next := make([]Device, 0, len(prev)+1)
	for _, x := range prev {
		if x.ID != d.ID {
			next = append(next, x)
		}
	}
	p.list = append(next, d)
	if err := p.saveLocked(); err != nil {
		p.list = prev
		return err
	}
	return nil
}

// update changes a paired phone, if it still is one (an Unpair during a
// sync wins).
func (p *pins) update(fp string, f func(*Device)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.list {
		if p.list[i].ID == fp {
			f(&p.list[i])
			_ = p.saveLocked() // a sync's record isn't worth failing it for
			return
		}
	}
}

// remove unpairs a phone: its pin is gone, so its next connection fails.
func (p *pins) remove(fp string) (Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, x := range p.list {
		if x.ID == fp {
			prev := p.list
			p.list = append(p.list[:i:i], p.list[i+1:]...)
			if err := p.saveLocked(); err != nil {
				p.list = prev
				return Device{}, err
			}
			return x, nil
		}
	}
	return Device{}, errors.New("no such phone")
}

// cleanText is a phone's text fit to show and log: printable, one line,
// at most max characters.
func cleanText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n >= max {
			break
		}
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// cleanName is a device name fit to show; "Phone" if nothing is left.
func cleanName(s string) string {
	if out := cleanText(s, maxDeviceName); out != "" {
		return out
	}
	return "Phone"
}
