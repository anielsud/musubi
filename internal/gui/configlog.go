package gui

import (
	"log"
	"strings"

	"github.com/anielsud/musubi/internal/config"
)

// editConfig is config.EditFile with a log line naming what changed, so a
// settings change that leads to trouble can be traced. It logs section and
// key names only, never values: some are tokens and API keys.
func editConfig(path string, edits []config.Edit) error {
	names := editNames(edits)
	if err := config.EditFile(path, edits); err != nil {
		log.Printf("config %s: changing %s failed: %v", path, names, err)
		return err
	}
	log.Printf("config %s: changed %s", path, names)
	return nil
}

// editNames lists the edits as "section.key" (or "section" for a whole
// table), with "(removed)" on deletions.
func editNames(edits []config.Edit) string {
	parts := make([]string, 0, len(edits))
	for _, e := range edits {
		n := e.Section
		if e.Key != "" {
			n += "." + e.Key
		}
		if e.Delete {
			n += " (removed)"
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, ", ")
}
