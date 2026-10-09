// Package tomoe reads Tomoe's saved sessions. It reads only session.json;
// audio and video never enter Musubi.
package tomoe

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/anielsud/musubi/internal/source"
)

const Name = "tomoe"

type Connector struct{ Dir string }

func (c Connector) Name() string { return Name }

func (c Connector) Fetch(ctx context.Context, emit func(source.Record) error) error {
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.Dir, e.Name(), "session.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := emit(source.Record{Source: Name, Ref: e.Name(), ContentType: "application/x-tomoe-session+json", Data: b}); err != nil {
			return err
		}
	}
	return nil
}
