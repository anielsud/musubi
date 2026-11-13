package phone

import (
	"sync"
	"time"
)

// Drafter runs the brief drafting an agenda request starts: one pass at a
// time, at most one every Every, counting down the events that had no
// brief when it started, which the agenda reports as "drafting".
type Drafter struct {
	// Every is the least time between the end of one pass and the start
	// of the next (the phone asks again while drafting is left).
	Every time.Duration

	mu      sync.Mutex
	running bool
	left    map[string]bool
	ended   time.Time
}

// Start returns how many briefs the running pass has left; with none
// running (and the last ended at least Every ago), it asks missing for
// the events without a brief and, if there are any, starts run in the
// background, which calls visited for each event it has looked at.
func (d *Drafter) Start(now time.Time, missing func() []string, run func(visited func(itemID string))) int {
	d.mu.Lock()
	if d.running {
		n := len(d.left)
		d.mu.Unlock()
		return n
	}
	if !d.ended.IsZero() && now.Sub(d.ended) < d.Every {
		d.mu.Unlock()
		return 0
	}
	d.running = true
	d.mu.Unlock()
	ids := missing()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ids) == 0 {
		d.running = false
		return 0
	}
	d.left = map[string]bool{}
	for _, id := range ids {
		d.left[id] = true
	}
	go func() {
		run(func(id string) {
			d.mu.Lock()
			delete(d.left, id)
			d.mu.Unlock()
		})
		d.mu.Lock()
		d.running, d.left, d.ended = false, nil, time.Now()
		d.mu.Unlock()
	}()
	return len(ids)
}
