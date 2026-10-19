package knowledge

import (
	"time"

	"github.com/anielsud/musubi/internal/store"
)

// Deriving "current" from the graph (docs/incremental-design.md): a
// statement in a thread is current as of a time unless a statement said
// then or later (and by that time) replaces, completes, answers or
// repeats it, or it is an open commitment or question older than
// lapseAfter. Nothing is carried forward, so inserting, correcting or
// deleting a statement only needs its own relations judged.

// Derivation is what DeriveCurrent reads.
type Derivation struct {
	AsOf     time.Time
	SaidAt   map[int64]time.Time
	Kind     map[int64]string
	Retiring map[int64][]int64 // statement → statements that retire it
}

// Current reports whether statement id is current as of d.AsOf.
func (d Derivation) Current(id int64) bool {
	said, ok := d.SaidAt[id]
	if !ok || said.After(d.AsOf) {
		return false
	}
	for _, by := range d.Retiring[id] {
		at, ok := d.SaidAt[by]
		if ok && !at.Before(said) && !at.After(d.AsOf) {
			return false
		}
	}
	if k := d.Kind[id]; (k == "commitment" || k == "question") && d.AsOf.Sub(said) > lapseAfter {
		return false
	}
	return true
}

// LoadDerivation reads what deriving "current" needs, as of the latest
// threaded item.
func LoadDerivation(st *store.Store) (Derivation, error) {
	var d Derivation
	var err error
	if d.SaidAt, d.Kind, err = st.StatementTimes(); err != nil {
		return d, err
	}
	if d.Retiring, err = st.RetiringRelations(); err != nil {
		return d, err
	}
	d.AsOf, err = st.LatestThreaded()
	return d, err
}

// DerivedCheck compares derived "current" with the stored flags.
type DerivedCheck struct {
	Memberships, Agree int
	// StoredRetiredDerivedCurrent counts retirements the graph can't
	// explain, by why they were retired (thread_items.retired_why).
	StoredRetiredDerivedCurrent map[string]int
	StoredCurrentDerivedRetired int
}

// CheckDerived derives "current" for every thread membership as of asOf
// (zero: the latest item threaded) and compares.
func CheckDerived(st *store.Store, asOf time.Time) (DerivedCheck, error) {
	c := DerivedCheck{StoredRetiredDerivedCurrent: map[string]int{}}
	d, err := LoadDerivation(st)
	if err != nil {
		return c, err
	}
	rows, err := st.ThreadItemRows()
	if err != nil {
		return c, err
	}
	// As of the latest item threaded (marked by the pipeline, or else the
	// latest statement in any thread).
	for _, r := range rows {
		if at := d.SaidAt[r.Statement]; at.After(d.AsOf) {
			d.AsOf = at
		}
	}
	if !asOf.IsZero() {
		d.AsOf = asOf
	}
	for _, r := range rows {
		c.Memberships++
		got := d.Current(r.Statement)
		switch {
		case got == r.Current:
			c.Agree++
		case got:
			why := r.RetiredWhy
			if why == "" {
				why = "unrecorded"
			}
			c.StoredRetiredDerivedCurrent[why]++
		default:
			c.StoredCurrentDerivedRetired++
		}
	}
	return c, nil
}
