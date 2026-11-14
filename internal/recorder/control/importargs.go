package control

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// eventIDPattern is what a calendar event's id (a Musubi item id,
// "source:ref" with long refs hashed) looks like; anything else is
// refused before it reaches a session file.
var eventIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:=+@-]{0,127}$`)

// ValidEventID reports whether id may name a calendar event.
func ValidEventID(id string) bool { return eventIDPattern.MatchString(id) }

// ValidImportID reports whether id is a canonical lowercase UUID, the
// only form an imported session's id (a folder name) may take.
func ValidImportID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id
}

// Check validates the args against the recorder's data folder: a
// canonical UUID (when given), a start time, a well-formed event id, and
// a plain file directly inside the inbox (no "..", no other folder).
func (p ImportArgs) Check(dataDir string) error {
	if p.ID != "" && !ValidImportID(p.ID) {
		return fmt.Errorf("bad import id")
	}
	if p.StartedAt.IsZero() {
		return fmt.Errorf("no start time")
	}
	if p.EventID != "" && !ValidEventID(p.EventID) {
		return fmt.Errorf("bad event id")
	}
	inbox, err := filepath.Abs(InboxDir(dataDir))
	if err != nil {
		return err
	}
	if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path || filepath.Dir(p.Path) != inbox ||
		strings.HasPrefix(filepath.Base(p.Path), ".") {
		return fmt.Errorf("the audio must be a file in the recorder's inbox")
	}
	return nil
}
