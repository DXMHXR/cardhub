package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		args  []string
		owner string
		num   int
		view  int
		ok    bool
	}{
		{[]string{"https://github.com/orgs/github/projects/4247"}, "github", 4247, 0, true},
		{[]string{"https://github.com/users/octocat/projects/2/views/6"}, "octocat", 2, 6, true},
		{[]string{"acme", "3"}, "acme", 3, 0, true},
		{[]string{"acme/3"}, "acme", 3, 0, true},
		{[]string{"acme", "x"}, "", 0, 0, false},
		{nil, "", 0, 0, false},
	}
	for _, c := range cases {
		o, n, v, err := ParseTarget(c.args)
		if (err == nil) != c.ok || o != c.owner || n != c.num || v != c.view {
			t.Errorf("ParseTarget(%v) = %q,%d,%d,%v", c.args, o, n, v, err)
		}
	}
}

func TestDiff(t *testing.T) {
	t0 := time.Now().Add(-time.Minute)
	item := func(id, status, state string, updated time.Time) Item {
		return Item{ID: id, Title: "t" + id, Number: 1, State: state, UpdatedAt: updated, Values: map[string]string{"Status": status}}
	}
	order := []string{"No Status", "Todo", "Doing", "Done"}
	// Windowed snapshots: only some cards loaded, totals cover the rest.
	prev := &Board{FetchedAt: t0, Order: order, Windowed: true,
		Totals: map[string]int{"Todo": 10, "Doing": 2, "Done": 50},
		Items:  []Item{item("a", "Todo", "OPEN", t0), item("c", "Done", "OPEN", t0)}}
	next := &Board{FetchedAt: time.Now(), Order: order, Windowed: true,
		// a: Todo→Doing (loaded both times). One unloaded card went Todo→Done.
		Totals: map[string]int{"Todo": 8, "Doing": 3, "Done": 51},
		Items: []Item{item("a", "Doing", "OPEN", time.Now()), item("c", "Done", "CLOSED", time.Now()),
			item("old", "Todo", "OPEN", t0.Add(-time.Hour)), item("fresh", "Todo", "OPEN", time.Now())}}

	var moves []string
	updated := map[string]bool{}
	for _, e := range Diff(prev, next, "Status") {
		switch e.Kind {
		case EvMoved:
			moves = append(moves, e.ItemID+":"+e.From+">"+e.To)
		case EvUpdated:
			updated[e.ItemID] = true
		}
	}
	if got := strings.Join(moves, ","); got != "a:Todo>Doing,:Todo>Done" {
		t.Errorf("moves = %s", got)
	}
	if !updated["c"] || !updated["fresh"] || updated["old"] {
		t.Errorf("updated = %v; want c (state change) and fresh (updated since last sync), not old (just scrolled in)", updated)
	}
	if Diff(nil, next, "Status") != nil {
		t.Error("first snapshot should produce no events")
	}
}

func TestFindView(t *testing.T) {
	vs := []ProjectView{{Number: 1, Name: "Sprint"}, {Number: 6, Name: "Bug"}}
	for arg, want := range map[string]int{"6": 1, "bug": 1, "Sprint": 0, "9": -1, "nope": -1} {
		if got := FindView(vs, arg); got != want {
			t.Errorf("FindView(%q) = %d, want %d", arg, got, want)
		}
	}
}

func TestMovesLightConnectors(t *testing.T) {
	status := SelectField{Name: "Status", Options: []Option{{"Todo", "GRAY"}, {"Doing", "BLUE"}, {"Review", "PURPLE"}, {"Done", "GREEN"}}}
	item := func(id, st string) Item {
		return Item{ID: id, Title: "card " + id, Number: 1, State: "OPEN", Values: map[string]string{"Status": st}}
	}
	snap := func(items ...Item) *Board {
		return &Board{Fields: []SelectField{status}, Items: items, FetchedAt: time.Now()}
	}
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.width, m.height = 88, 60 // 2 columns per grid row: Todo→Doing ↩ Review→Done
	m.applyBoard(snap(item("a", "Todo"), item("b", "Done"), item("c", "Todo")))
	if n, _ := m.traffic(0); n != 0 {
		t.Fatal("no moves yet, connector should be idle")
	}
	// a: Todo→Review crosses connectors 0 and 1 (the wrap); b: Done→Doing goes back over 2 and 1.
	m.applyBoard(snap(item("a", "Review"), item("b", "Doing"), item("c", "Todo")))

	for k, want := range []struct{ n, dir int }{{1, 1}, {2, 0}, {1, -1}} {
		n, dir := m.traffic(k)
		if n != want.n || (want.dir != 0 && dir != want.dir) {
			t.Errorf("connector %d: got n=%d dir=%d, want %+v", k, n, dir, want)
		}
	}
	out := m.boardView(50)
	if !strings.Contains(out, "×2") || !strings.Contains(out, "×1") {
		t.Errorf("lit connectors missing from render:\n%s", out)
	}
	t.Log("\n" + out)

	m.moves[0].at = time.Now().Add(-2 * flashFor) // a's move has expired
	if n, _ := m.traffic(0); n != 0 {
		t.Error("expired move should no longer light the connector")
	}
}

func TestAutoInterval(t *testing.T) {
	now := time.Now()
	fresh := RateLimit{Remaining: 5000, Limit: 5000, ResetAt: now.Add(time.Hour)}
	cases := []struct {
		name string
		cost int
		rl   RateLimit
		want time.Duration
	}{
		{"tiny view stays at base", 1, fresh, 30 * time.Second},
		{"bug view (6 pts) fits at base", 6, fresh, 30 * time.Second},
		{"all items (12 pts) slows to ~60s", 12, fresh, 60 * time.Second},
		{"budget mostly used by others", 6, RateLimit{Remaining: 600, Limit: 5000, ResetAt: now.Add(40 * time.Minute)}, 50 * time.Second},
		{"nearly exhausted backs off hard", 1, RateLimit{Remaining: 100, Limit: 5000, ResetAt: now.Add(30 * time.Minute)}, 2 * time.Minute},
		{"huge board capped at max", 200, fresh, maxInterval},
	}
	for _, c := range cases {
		if got := autoInterval(30*time.Second, c.cost, c.rl, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestMatchProject(t *testing.T) {
	refs := []ProjectRef{{"acme", 1, "Roadmap"}, {"acme", 2, "Roadmap Archive"}, {"acme", 3, "Bug Triage"}, {"beta", 1, "Bugs"}}
	for name, want := range map[string]int{"roadmap": 1, "ROADMAP ARCHIVE": 2, "triage": 3} {
		if p, err := matchProject(refs, name); err != nil || p.Number != want {
			t.Errorf("matchProject(%q) = %+v, %v; want #%d", name, p, err, want)
		}
	}
	for _, name := range []string{"bug", "nope"} { // ambiguous, missing
		if _, err := matchProject(refs, name); err == nil {
			t.Errorf("matchProject(%q) should fail", name)
		}
	}
}
