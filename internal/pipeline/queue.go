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
	// New: items newer than the frontier, or behind it by no more than
	// threads.rewind threaded items (those are threaded again after): to
	// extract (then thread), or extracted and waiting to be threaded. An
	// update after new data takes AutoMax of them; "Catch up" the rest.
	NewExtract int `json:"newExtract"`
	NewThread  int `json:"newThread"`
	AutoMax    int `json:"autoMax"`
	// Older: the rest (a backlog), not yet extracted or extracted but
	// unthreaded. They join only when everything is rethreaded in time
	// order.
	OlderExtract int `json:"olderExtract"`
	OlderThread  int `json:"olderThread"`
	// Duplicates: meetings recorded twice; one of each becomes knowledge.
	Duplicates int `json:"duplicates"`
	// Estimates, in dollars and hours.
	CatchUpUSD    float64 `json:"catchUpUsd"`
	RethreadUSD   float64 `json:"rethreadUsd"`
	RethreadHours float64 `json:"rethreadHours"`
	// Upgrades is work a new version left for the owner, as the last
	// update found it (knowledge.hold_upgrades, knowledge.updates).
	Upgrades Upgrades `json:"upgrades"`
}

// Upgrades is what a new version (of the code or a model) would redo on
// existing data at a model's cost, waiting to be applied.
type Upgrades struct {
	// Passages: meetings a new chunker would cut again, and so re-extract.
	// Extract: items extracted under an earlier extraction version.
	Passages int `json:"passages"`
	Extract  int `json:"extract"`
	// Digest: meetings summarised under an earlier digest version.
	Digest int `json:"digest"`
	// Statuses: thread statuses under an earlier status version.
	Statuses int `json:"statuses"`
	// Names: meeting names under an earlier naming version or model.
	Names int     `json:"names"`
	USD   float64 `json:"usd"`
}

// centsName: one short call; centsDigest: a whole-meeting read, about
// the cost of extraction (both rough, for estimates).
const centsName, centsDigest = 0.1, 1.0

// Redo are the stages applying the upgrades redoes (--redo).
func (u Upgrades) Redo() []string {
	var out []string
	if u.Passages+u.Extract > 0 {
		out = append(out, "extract")
	}
	if u.Digest > 0 {
		out = append(out, "digest")
	}
	if u.Statuses > 0 {
		out = append(out, "statuses")
	}
	if u.Names > 0 {
		out = append(out, "names")
	}
	return out
}

func (r *Runner) upgrades() Upgrades {
	u := Upgrades{Passages: r.waiting("passages"), Extract: r.waiting("extract"), Digest: r.waiting("digest"),
		Statuses: r.waiting("statuses"), Names: r.waiting("names")}
	u.USD = u.Cost()
	return u
}

// UpgradesOf is the waiting upgrade work a run reported.
func UpgradesOf(o Outdated) Upgrades {
	u := Upgrades{Passages: o.Passages, Extract: o.Extract, Digest: o.Digest, Statuses: o.Statuses, Names: o.Names}
	u.USD = u.Cost()
	return u
}

// Cost estimates applying them, in dollars.
func (u Upgrades) Cost() float64 {
	// A re-extracted item is threaded again, and its threads' statuses
	// rewritten; the statuses counted here are those of other threads.
	read := float64(u.Passages + u.Extract)
	return (read*(centsExtract+centsThread+centsLabelStatus) + float64(u.Digest)*centsDigest +
		float64(u.Statuses)*centsLabelStatus + float64(u.Names)*centsName) / 100
}

// Queue reports the work waiting: what an update will take on its own,
// and what needs a run started by hand.
func (r *Runner) Queue() (Queue, error) {
	kc := r.Cfg.Knowledge
	q := Queue{Extract: kc.Extract, AutoMax: kc.AutoMax, Upgrades: r.upgrades()}
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
	times, err := r.threadedTimes(items)
	if err != nil {
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
		newer := newerThan(times, it.OccurredAt) <= kc.Threads.Rewind
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
