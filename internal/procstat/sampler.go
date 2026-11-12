package procstat

import (
	"sync"
	"time"
)

// Part is one process's reading.
type Part struct {
	Name string `json:"name"`
	PID  int    `json:"pid"`
	// CPU is the share of one core over the last interval, in percent
	// (100 = one core fully busy; a 10-core Mac goes to 1000).
	CPU       float64 `json:"cpu"`
	RSS       uint64  `json:"rss"`
	Footprint uint64  `json:"footprint"`
}

// Reading is a component's processes summed, with each listed.
type Reading struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	CPU       float64 `json:"cpu"`
	RSS       uint64  `json:"rss"`
	Footprint uint64  `json:"footprint"`
	Parts     []Part  `json:"parts"`
}

// Sample is every component at one moment.
type Sample struct {
	At         time.Time `json:"at"`
	Components []Reading `json:"components"`
}

// Snapshot is what the panel opens with.
type Snapshot struct {
	Supported  bool     `json:"supported"`
	Reason     string   `json:"reason,omitempty"`
	Machine    Machine  `json:"machine"`
	IntervalMs int64    `json:"intervalMs"`
	History    []Sample `json:"history"`
}

const (
	// Interval: often enough to see a model run, rare enough that reading
	// the process table costs nothing noticeable.
	Interval = 2 * time.Second
	// Keep: 15 minutes of samples.
	Keep = int(15 * time.Minute / Interval)
	// Lease: the panel renews its watch more often than this; a window
	// that went away without saying so stops the sampler after it.
	Lease = 45 * time.Second
)

// Sampler samples the components while someone watches and keeps the last
// Keep samples. It does nothing between watches.
type Sampler struct {
	src         Source
	self        int
	recorderPID func() int
	onSample    func(Sample)
	machine     Machine

	// Overridable in tests.
	interval time.Duration
	lease    time.Duration
	now      func() time.Time

	mu      sync.Mutex
	ring    *Ring[Sample]
	prev    map[int]prevUsage
	prevAt  time.Time
	stop    chan struct{}
	done    chan struct{}
	leaseTo time.Time
}

type prevUsage struct {
	seen  bool
	start time.Time
	cpu   time.Duration
}

// NewSampler: self is this process; recorderPID (may be nil) returns the
// pid the recorder reported; onSample (may be nil) gets each sample.
func NewSampler(src Source, m Machine, self int, recorderPID func() int, onSample func(Sample)) *Sampler {
	return &Sampler{src: src, machine: m, self: self, recorderPID: recorderPID, onSample: onSample,
		interval: Interval, lease: Lease, now: time.Now, ring: NewRing[Sample](Keep)}
}

// Watch starts sampling, or renews the watch, and returns the history.
func (s *Sampler) Watch() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseTo = s.now().Add(s.lease)
	if s.stop == nil {
		s.stop, s.done = make(chan struct{}), make(chan struct{})
		// A baseline, so the first sample shown already has CPU rates.
		s.sampleLocked()
		go s.run(s.stop, s.done)
	}
	return Snapshot{Supported: true, Machine: s.machine, IntervalMs: s.interval.Milliseconds(), History: s.ring.Slice()}
}

// Unwatch stops sampling; the history is kept.
func (s *Sampler) Unwatch() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}

// Running says whether the sampler is sampling.
func (s *Sampler) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop != nil
}

func (s *Sampler) run(stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		s.mu.Lock()
		if s.stop != stop {
			s.mu.Unlock()
			return
		}
		if s.now().After(s.leaseTo) {
			s.stop, s.done = nil, nil
			s.mu.Unlock()
			return
		}
		smp, ok := s.sampleLocked()
		cb := s.onSample
		s.mu.Unlock()
		if ok && cb != nil {
			cb(smp)
		}
	}
}

// sampleLocked reads the table once. ok is false for the baseline (no
// earlier reading to take CPU time from) and when the table can't be read.
func (s *Sampler) sampleLocked() (Sample, bool) {
	now := s.now()
	procs, err := s.src.Procs()
	if err != nil {
		return Sample{}, false
	}
	rec := 0
	if s.recorderPID != nil {
		rec = s.recorderPID()
	}
	groups := Find(procs, s.src, s.self, rec)
	// After a stop the old baseline is stale: start over.
	baseline := s.prevAt.IsZero() || now.Sub(s.prevAt) > 3*s.interval
	wall := now.Sub(s.prevAt)
	next := map[int]prevUsage{}
	smp := Sample{At: now}
	for _, g := range groups {
		r := Reading{Key: g.Key, Name: g.Name}
		for _, m := range g.Members {
			u, err := s.src.Usage(m.PID)
			if err != nil {
				continue
			}
			next[m.PID] = prevUsage{seen: true, start: m.Proc.Start, cpu: u.CPU}
			var cpu float64
			if !baseline {
				cpu = cpuPercent(s.prev[m.PID], m.Proc.Start, s.prevAt, u.CPU, wall)
			}
			r.Parts = append(r.Parts, Part{Name: m.Name, PID: m.PID, CPU: cpu, RSS: u.RSS, Footprint: u.Footprint})
			r.CPU += cpu
			r.RSS += u.RSS
			r.Footprint += u.Footprint
		}
		if len(r.Parts) > 0 {
			smp.Components = append(smp.Components, r)
		}
	}
	s.prev, s.prevAt = next, now
	if baseline {
		return smp, false
	}
	s.ring.Push(smp)
	return smp, true
}

// cpuPercent is the CPU time a process used over the interval as a share
// of one core: the change in its cumulative time, not a decaying average
// like ps's %cpu. A process that started within the interval used all its
// time in it. One seen before only under another start (a reused pid) is
// the same new-process case. One that was running but not read last time
// (unreadable then) gets 0 for this interval rather than its lifetime.
func cpuPercent(prev prevUsage, start, prevAt time.Time, cpu, wall time.Duration) float64 {
	if wall <= 0 {
		return 0
	}
	var used time.Duration
	switch {
	case prev.seen && prev.start.Equal(start):
		used = cpu - prev.cpu
	case start.After(prevAt):
		used = cpu
	default:
		return 0
	}
	if used < 0 {
		return 0
	}
	return float64(used) / float64(wall) * 100
}
