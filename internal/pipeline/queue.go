package pipeline

import (
	"time"

	"github.com/anielsud/musubi/internal/item"
)

// Costs per meeting, for estimates (docs/setup.md, "What processing
// costs"; docs/research-log.md): lean extraction on GLM-5.3-Flash 0.9¢;
// threading 2.7¢ measured at 165 meetings, taken as 3¢ since threads grow;
// labels and statuses about 1¢. Threading runs one meeting after another,
// 1.5 minutes each measured, taken as 2.
const (
	centsExtract     = 0.9
	centsThread      = 3.0
	centsLabelStatus = 1.0
	minutesThread    = 2.0
)

// Queue is the work waiting in the knowledge layer.
type Queue struct {
	// Extract is knowledge.extract ("none": items arrive by import only).
	Extract string `json:"extract"`
	// Frontier is when the newest threaded meeting happened: newer items
	// append to the threads in time order, older ones can't.
	Frontier time.Time `json:"frontier"`
	Threaded int       `json:"threaded"`
	// New: items newer than the frontier, to extract (then thread), or
	// extracted and waiting to be threaded. An update after new data takes
	// AutoMax of them; "Catch up" takes the rest.
	NewExtract int `json:"newExtract"`
	NewThread  int `json:"newThread"`
	AutoMax    int `json:"autoMax"`
	// Older: items older than the frontier (a backlog), not yet extracted
	// or extracted but unthreaded. They join only when everything is
	// rethreaded in time order.
	OlderExtract int `json:"olderExtract"`
	OlderThread  int `json:"olderThread"`
	// Duplicates: meetings recorded twice; one of each becomes knowledge.
	Duplicates int `json:"duplicates"`
	// Estimates, in dollars and hours.
	CatchUpUSD    float64 `json:"catchUpUsd"`
	RethreadUSD   float64 `json:"rethreadUsd"`
	RethreadHours float64 `json:"rethreadHours"`
}

// Queue reports the work waiting: what an update will take on its own,
// and what needs a run started by hand.
func (r *Runner) Queue() (Queue, error) {
	kc := r.Cfg.Knowledge
	q := Queue{Extract: kc.Extract, AutoMax: kc.AutoMax}
	items, err := r.Store.AllItems()
	if err != nil {
		return q, err
	}
	done, err := r.Store.ThreadedItems()
	if err != nil {
		return q, err
	}
	if q.Frontier, err = r.threadFrontier(items); err != nil {
		return q, err
	}
	extracted := func(id string) bool {
		s, _ := r.Store.StageStamp("extract", id)
		return s != ""
	}
	stitchedItems, into, err := r.stitches(items, false)
	if err != nil {
		return q, err
	}
	for i := range items {
		if s, ok := stitchedItems[items[i].ID]; ok {
			items[i] = s
		}
	}
	dups := withStitches(meetingDuplicates(items, kc.PreferSource, extracted), into)
	q.Duplicates = len(dups)
	for i := range items {
		it := &items[i]
		if it.Kind == item.KindEvent || dups[it.ID] != "" {
			continue
		}
		threaded := done[it.ID] != ""
		if threaded {
			q.Threaded++
			continue
		}
		ex := extracted(it.ID) || kc.Extract == "none"
		newer := q.Frontier.IsZero() || it.OccurredAt.After(q.Frontier)
		switch {
		case newer && !ex:
			q.NewExtract++
		case newer:
			q.NewThread++
		case !ex:
			q.OlderExtract++
		default:
			q.OlderThread++
		}
	}
	per := centsThread + centsLabelStatus
	newN := q.NewExtract + q.NewThread
	q.CatchUpUSD = (float64(q.NewExtract)*centsExtract + float64(newN)*per) / 100
	// A rethread takes the preferred copy of a meeting recorded twice, which
	// may not be extracted yet.
	switching := 0
	for id, kept := range withStitches(meetingDuplicates(items, kc.PreferSource, func(string) bool { return false }), into) {
		_ = id
		if !extracted(kept) {
			switching++
		}
	}
	all := q.Threaded + newN + q.OlderExtract + q.OlderThread
	q.RethreadUSD = (float64(q.NewExtract+q.OlderExtract+switching)*centsExtract + float64(all)*per) / 100
	q.RethreadHours = float64(all) * minutesThread / 60
	if kc.Extract == "none" {
		q.CatchUpUSD -= float64(q.NewExtract) * centsExtract / 100
		q.RethreadUSD -= float64(q.NewExtract+q.OlderExtract) * centsExtract / 100
	}
	return q, nil
}
