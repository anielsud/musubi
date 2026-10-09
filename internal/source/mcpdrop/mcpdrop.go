// Package mcpdrop reads an inbox directory that other tools fill: scheduled
// Claude Code jobs using the approved M365 / Salesforce / Zoom connectors,
// or test fixtures. Each file is one envelope:
//
//	{"kind": "event|email|chat", "source": "<name>", "ref": "<stable id>", "record": {...}}
//
// Files live in <inbox>/<source>/*.json. The record's shape per kind is
// documented in internal/item/drop.go.
package mcpdrop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/anielsud/musubi/internal/source"
)

const ContentType = "application/x-musubi-drop+json"

type Connector struct{ Dir string }

func (c Connector) Name() string { return "mcpdrop" }

func (c Connector) Fetch(ctx context.Context, emit func(source.Record) error) error {
	files, err := filepath.Glob(filepath.Join(c.Dir, "*", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var env struct{ Source, Ref string }
		if err := json.Unmarshal(b, &env); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if env.Source == "" || env.Ref == "" {
			return fmt.Errorf("%s: envelope needs source and ref", f)
		}
		if err := emit(source.Record{Source: env.Source, Ref: env.Ref, ContentType: ContentType, Data: b}); err != nil {
			return err
		}
	}
	return nil
}
