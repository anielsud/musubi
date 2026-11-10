// Package item defines the one shape every derive stage consumes, and the
// per-source translators that produce it from raw records.
package item

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anielsud/musubi/internal/source"
	"github.com/anielsud/musubi/internal/source/mcpdrop"
)

const tomoeContentType = "application/x-tomoe-session+json"

const (
	KindMeeting = "meeting"
	KindEvent   = "event"
	KindEmail   = "email"
	KindChat    = "chat"
)

// Item is a normalised unit of content: a meeting, an email, a chat
// conversation, a calendar event.
type Item struct {
	ID         string
	Source     string
	Ref        string
	RawHash    string
	Kind       string
	Title      string
	OccurredAt time.Time
	EndedAt    time.Time
	Thread     string
	// Segments are the item's text in order: meeting lines, chat messages,
	// an email body. Chunking and mention spans refer to them by index.
	Segments     []Segment
	Participants []Participant
	// Native holds source metadata the rules read, notably "sensitivity".
	Native map[string]string
}

// End is when the item ends: its end time, else its last line's (some
// imported transcripts carry only a start).
func (it *Item) End() time.Time {
	if !it.EndedAt.IsZero() && it.EndedAt.After(it.OccurredAt) {
		return it.EndedAt
	}
	end := 0.0
	for _, s := range it.Segments {
		end = max(end, s.End, s.Start)
	}
	return it.OccurredAt.Add(time.Duration(end * float64(time.Second)))
}

type Segment struct {
	Speaker string  `json:"speaker,omitempty"` // display name; "You" is the owner
	Email   string  `json:"email,omitempty"`
	Start   float64 `json:"start,omitempty"` // seconds from OccurredAt
	End     float64 `json:"end,omitempty"`
	Text    string  `json:"text"`
	// From is set on a line taken from another copy of the meeting to fill
	// a stretch this recording missed ("teams": the Teams transcript, for
	// before the owner joined or after they left).
	From string `json:"from,omitempty"`
}

type Participant struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
	Role  string `json:"role"` // speaker, attendee, organizer, from, to, cc, member
	Owner bool   `json:"owner,omitempty"`
}

// ContentHash covers everything stages read, so a re-fetched record that
// changes nothing material doesn't trigger reprocessing.
func (it Item) ContentHash() string {
	b, _ := json.Marshal(struct {
		K, T string
		O, E int64
		Th   string
		S    []Segment
		P    []Participant
		N    map[string]string
	}{it.Kind, it.Title, it.OccurredAt.Unix(), it.EndedAt.Unix(), it.Thread, it.Segments, it.Participants, it.Native})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// ID builds a stable item id from source and ref, hashing long refs (Graph
// ids run to ~150 characters).
func ID(src, ref string) string {
	if len(ref) <= 40 && !strings.ContainsAny(ref, " /") {
		return src + ":" + ref
	}
	h := sha256.Sum256([]byte(ref))
	return src + ":" + hex.EncodeToString(h[:10])
}

// Translate turns one raw record into items.
func Translate(rec source.Record, rawHash string) ([]Item, error) {
	var items []Item
	var err error
	switch rec.ContentType {
	case tomoeContentType:
		items, err = fromTomoe(rec.Data)
	case mcpdrop.ContentType:
		items, err = fromDrop(rec.Data)
	default:
		return nil, fmt.Errorf("%s/%s: no translator for %q", rec.Source, rec.Ref, rec.ContentType)
	}
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", rec.Source, rec.Ref, err)
	}
	for i := range items {
		items[i].Source, items[i].Ref, items[i].RawHash = rec.Source, rec.Ref, rawHash
		items[i].ID = ID(rec.Source, rec.Ref)
		if items[i].Native == nil {
			items[i].Native = map[string]string{}
		}
	}
	return items, nil
}
