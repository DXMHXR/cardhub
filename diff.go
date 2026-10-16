package main

import (
	"time"
)

type EventKind int

const (
	EvAdded EventKind = iota
	EvRemoved
	EvMoved
	EvUpdated
)

// Event is one change detected between two consecutive board snapshots.
type Event struct {
	At     time.Time
	Kind   EventKind
	ItemID string // empty for moves inferred from column totals
	From   string // EvMoved only: source and destination column
	To     string
}

// Diff compares two snapshots grouped by groupField. Snapshots may be windowed
// (only the first cards of each column loaded), so it works in two layers:
//
//  1. Cards loaded in both snapshots are compared exactly: column moves and
//     state/title/field edits.
//  2. Whatever the column totals still can't account for is inferred: a column
//     that shrank and one that grew are paired up as a move between them.
//
// Cards loaded for the first time count as changed only if GitHub says they were
// updated since the previous snapshot (so scrolling doesn't flag cards).
// Both snapshots need Totals and Order.
func Diff(prev, next *Board, groupField string) []Event {
	if prev == nil {
		return nil
	}
	at := next.FetchedAt
	old := make(map[string]Item, len(prev.Items))
	for _, it := range prev.Items {
		old[it.ID] = it
	}
	delta := map[string]int{}
	for name, n := range next.Totals {
		delta[name] += n
	}
	for name, n := range prev.Totals {
		delta[name] -= n
	}

	var evs []Event
	for _, it := range next.Items {
		o, ok := old[it.ID]
		if !ok {
			if it.UpdatedAt.After(prev.FetchedAt) {
				evs = append(evs, Event{At: at, Kind: EvUpdated, ItemID: it.ID})
			}
			continue
		}
		if a, b := colName(o, groupField), colName(it, groupField); a != b {
			evs = append(evs, Event{At: at, Kind: EvMoved, ItemID: it.ID, From: a, To: b})
			delta[a]++ // explained: don't infer this one again from the totals
			delta[b]--
			continue
		}
		if o.State != it.State || o.Title != it.Title || !sameValues(o.Values, it.Values) {
			evs = append(evs, Event{At: at, Kind: EvUpdated, ItemID: it.ID})
		}
	}

	// Pair the remaining shrinks with growths, in column order.
	var from, to []string
	for _, name := range next.Order {
		for d := delta[name]; d < 0; d++ {
			from = append(from, name)
		}
		for d := delta[name]; d > 0; d-- {
			to = append(to, name)
		}
	}
	for i := 0; i < len(from) && i < len(to); i++ {
		evs = append(evs, Event{At: at, Kind: EvMoved, From: from[i], To: to[i]})
	}
	return evs
}

func sameValues(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func colName(it Item, field string) string {
	if v := it.Values[field]; v != "" {
		return v
	}
	return "No " + field
}

// tally fills in Totals and Order for a full (non-windowed) snapshot.
func tally(b *Board, groupField string) {
	b.Totals = map[string]int{}
	b.Order = []string{"No " + groupField}
	for _, f := range b.Fields {
		if f.Name == groupField {
			for _, o := range f.Options {
				b.Order = append(b.Order, o.Name)
			}
		}
	}
	for _, it := range b.Items {
		b.Totals[colName(it, groupField)]++
	}
}
