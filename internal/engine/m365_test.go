package engine

import (
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/item"
)

func TestOverlappingTeamsCopy(t *testing.T) {
	at := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	m := &item.Item{OccurredAt: at, EndedAt: at.Add(40 * time.Minute)}
	short := &item.Item{ID: "short", OccurredAt: at.Add(35 * time.Minute), EndedAt: at.Add(50 * time.Minute)} // 5 min shared
	main := &item.Item{ID: "main", OccurredAt: at.Add(time.Minute), EndedAt: at.Add(45 * time.Minute)}
	if got := overlapping(m, []*item.Item{short, main}); got == nil || got.ID != "main" {
		t.Errorf("got %v", got)
	}
	if got := overlapping(m, []*item.Item{short}); got != nil {
		t.Errorf("a copy sharing under a quarter of the recording was taken: %v", got.ID)
	}
}
