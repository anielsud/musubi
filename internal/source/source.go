// Package source defines connectors: they fetch records and hand them over
// unchanged. Interpretation belongs to internal/item.
package source

import "context"

// Record is one unit of raw input. Source+Ref identify it across fetches, so
// a changed record replaces its earlier version.
type Record struct {
	Source      string
	Ref         string
	ContentType string
	Data        []byte
}

type Connector interface {
	Name() string
	Fetch(ctx context.Context, emit func(Record) error) error
}
