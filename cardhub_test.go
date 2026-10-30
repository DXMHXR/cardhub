package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"os"
	"path/filepath"
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
		b := &Board{Fields: []SelectField{status}, Items: items, FetchedAt: time.Now(), Windowed: true,
			Order: []string{"No Status", "Todo", "Doing", "Review", "Done"}, Totals: map[string]int{}}
		for _, it := range items {
			b.Totals[it.Values["Status"]]++
		}
		return b
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
	lipgloss.SetColorProfile(termenv.TrueColor) // to tell the white streaming dot apart
	out := m.boardView(m.width, 50)
	lipgloss.SetColorProfile(termenv.Ascii)
	if white := strings.Count(out, "255;255;255m•"); white != 3 || strings.Contains(out, "×") {
		t.Errorf("want a white dot on each of the 3 lit connectors, got %d:\n%s", white, ansi.Strip(out))
	}
	t.Log("\n" + ansi.Strip(out))

	m.moves[0].at = time.Now().Add(-time.Hour) // connectors stay lit all session
	if n, _ := m.traffic(0); n == 0 {
		t.Error("an old move should still light the connector")
	}
}

func TestStatsCountsMovesAcrossBoards(t *testing.T) {
	status := SelectField{Name: "Status", Options: []Option{{"Todo", "GRAY"}, {"Review", "PURPLE"}, {"Done", "GREEN"}}}
	snap := func(at map[string]string) *Board {
		b := &Board{Fields: []SelectField{status}, FetchedAt: time.Now(), Windowed: true,
			Order: []string{"No Status", "Todo", "Review", "Done"}, Totals: map[string]int{}}
		for _, id := range []string{"a", "b"} {
			if st, ok := at[id]; ok {
				b.Items = append(b.Items, Item{ID: id, Title: "card " + id, Number: 7, Values: map[string]string{"Status": st}})
				b.Totals[st]++
			}
		}
		return b
	}
	before, after := snap(map[string]string{"a": "Todo", "b": "Done"}), snap(map[string]string{"a": "Review", "b": "Done"})

	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.width, m.height = 120, 40
	m.applyBoard(before)
	m.applyBoard(after)
	// a split showing the same project: its board sees the same move, counted once
	m.other, m.split = newPane("o", 1, "", ""), true
	m.onOther(func() tea.Cmd { m.applyBoard(before); m.applyBoard(after); return nil })
	if s := m.moveLog.sections; len(s) != 1 || s[0].total != 1 {
		t.Fatalf("same project in both boards: want 1 section with 1 move, got %+v", s)
	}
	// the split's board on another project counts on its own
	m.other = newPane("p", 2, "", "")
	m.onOther(func() tea.Cmd {
		m.applyBoard(snap(map[string]string{"b": "Todo"}))
		m.applyBoard(snap(map[string]string{"b": "Done"}))
		return nil
	})
	out := ansi.Strip(m.statsView(30))
	for _, want := range []string{"2 moves in 2 projects", "o/1", "p/2", "Todo → Review", "Todo → Done", "#7 card a"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats screen lacks %q:\n%s", want, out)
		}
	}
	t.Log("\n" + out)
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

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"v0.1.0": true, "v1.2.3": true, "(devel)": false, "": false,
		"v0.0.0-20261016194020-95a00727c30f":       false,
		"v0.0.0-20261016194020-95a00727c30f+dirty": false,
		"v0.1.1-0.20261016194020-95a00727c30f":     false,
	} {
		if got := isRelease(v); got != want {
			t.Errorf("isRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestSkillInstallsOnlyForAgentsPresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.MkdirAll(filepath.Join(home, ".codex", "skills"), 0o755)
	if code := installSkill(); code != exitOK {
		t.Fatalf("install exit %d", code)
	}
	for _, p := range []string{".claude/skills/cardhub/SKILL.md", ".codex/skills/cardhub/SKILL.md"} {
		if b, err := os.ReadFile(filepath.Join(home, p)); err != nil || !strings.Contains(string(b), "cardhub guide") {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor")); err == nil {
		t.Error("shouldn't create a home for an agent CLI that isn't installed")
	}
	uninstallSkill()
	if _, err := os.Stat(filepath.Join(home, ".claude/skills/cardhub")); err == nil {
		t.Error("uninstall should remove the skill")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cur, tag string
		want     bool
	}{
		{"v0.2.2", "v0.2.3", true}, {"v0.2.2", "v0.3.0", true}, {"v0.2.2", "v1.0.0", true},
		{"v0.2.2", "v0.2.2", false}, {"v0.2.3", "v0.2.2", false}, {"v0.10.0", "v0.9.9", false},
		{"v0.2.1+dirty", "v0.2.2", true}, {"v0.2.2", "", false}, {"(devel)", "v0.2.2", false},
	} {
		if got := newer(c.cur, c.tag); got != c.want {
			t.Errorf("newer(%q, %q) = %v, want %v", c.cur, c.tag, got, c.want)
		}
	}
}

func TestDistinctColors(t *testing.T) {
	cols := []column{{name: "Backlog", color: "GRAY"}, {name: "Todo", color: "BLUE"}, {name: "Doing", color: "BLUE"},
		{name: "Review", color: ""}, {name: "QA", color: "GREEN"}, {name: "Done", color: "GRAY"}}
	distinctColors(cols)
	seen := map[string]string{}
	for _, c := range cols {
		if c.color == "GRAY" || ghColors[c.color] == "" {
			t.Errorf("%s got %q: want a real, non-grey colour", c.name, c.color)
		}
		if other, ok := seen[c.color]; ok {
			t.Errorf("%s and %s share %s", other, c.name, c.color)
		}
		seen[c.color] = c.name
	}
	if cols[1].color != "BLUE" {
		t.Errorf("a column with a unique GitHub colour should keep it: %+v", cols)
	}
	if cols[5].color != "GREEN" || cols[4].color == "GREEN" {
		t.Errorf("only the last column (done) should be green: %+v", cols)
	}
}

func TestColumnShown(t *testing.T) {
	sprint := `-status:"Ready for Test","In Test","Waiting Deploy" -label:qa-feedback`
	for _, c := range []struct {
		filter, col string
		want        bool
	}{
		{sprint, "Backlog", true}, {sprint, "In Progress", true}, {sprint, "Done", true},
		{sprint, "Ready for Test", false}, {sprint, "In Test", false}, {sprint, "Waiting Deploy", false},
		{`status:Todo,"In Progress"`, "In Progress", true}, {`status:Todo,"In Progress"`, "Done", false},
		{"no:status", "No Status", true}, {"no:status", "Todo", false},
		{"has:status", "No Status", false}, {"has:status", "Todo", true},
		{"label:bug", "Todo", true}, {"", "No Status", true},
	} {
		if got := columnShown(c.filter, "Status", c.col); got != c.want {
			t.Errorf("columnShown(%q, %q) = %v, want %v", c.filter, c.col, got, c.want)
		}
	}
}

func TestSplitLayout(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // W saves the focused board as the last one
	t.Setenv("XDG_CONFIG_HOME", "")
	status := SelectField{Name: "Status", Options: []Option{{"Todo", "GRAY"}, {"Doing", "BLUE"}, {"Done", "GREEN"}}}
	snap := func(title string) *Board {
		it := Item{ID: title, Title: "card of " + title, Number: 1, State: "OPEN", Values: map[string]string{"Status": "Todo"}}
		return &Board{Title: title, Fields: []SelectField{status}, Items: []Item{it}, FetchedAt: time.Now(),
			Order: []string{"Todo", "Doing", "Done"}, Totals: map[string]int{"Todo": 1}}
	}
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.applyBoard(snap("Alpha"))
	m.openSplit(ProjectRef{Owner: "o", Number: 2})
	m.applyBoard(snap("Beta"))
	if !m.split || m.board.Title != "Beta" || m.other.board.Title != "Alpha" {
		t.Fatal("the second board should open focused, next to the first")
	}

	for _, tc := range []struct {
		w      int
		across bool
	}{{160, true}, {100, false}} {
		m.width, m.height = tc.w, 50
		if m.splitAcross() != tc.across {
			t.Errorf("width %d: across=%v, want %v", tc.w, m.splitAcross(), tc.across)
		}
		out := ansi.Strip(m.View())
		lines := strings.Split(out, "\n")
		if len(lines) != m.height {
			t.Errorf("width %d: %d lines, want %d", tc.w, len(lines), m.height)
		}
		for i, l := range lines {
			if ansi.StringWidth(l) > tc.w {
				t.Errorf("width %d: line %d is %d wide", tc.w, i, ansi.StringWidth(l))
			}
		}
		a, b := strings.Index(out, "○ Alpha"), strings.Index(out, "● Beta")
		if a < 0 || b < 0 {
			t.Fatalf("width %d: want both board titles, Beta focused:\n%s", tc.w, out)
		}
		sameLine := !strings.Contains(out[min(a, b):max(a, b)], "\n")
		if sameLine != tc.across {
			t.Errorf("width %d: titles on one line=%v, want %v", tc.w, sameLine, tc.across)
		}
		t.Log("\n" + out)
	}

	// the focused board (Beta, on the right) gets its view menu over it, named
	m.width = 160
	m.views = []ProjectView{{Name: "Bug", Number: 6}}
	m.openPicker(pickView)
	for _, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(l, "Switch view"); i >= 0 {
			if ansi.StringWidth(l[:i]) < m.width/2 || !strings.Contains(l, "Beta") {
				t.Errorf("the view menu should open over Beta, on the right:\n%s", l)
			}
		}
	}
	m.picker.open = false

	m.width = 160
	m.swapBoards() // Beta moves to the left, still focused
	out := ansi.Strip(m.View())
	if a, b := strings.Index(out, "○ Alpha"), strings.Index(out, "● Beta"); b < 0 || a < b || strings.Contains(out[b:a], "\n") {
		t.Errorf("after B, Beta should be first (left), Alpha next to it:\n%s", out)
	}
	m.swapBoards()

	m.switchBoard()
	if m.board.Title != "Alpha" {
		t.Error("W should focus the other board")
	}
	m.toggleSplit()
	if m.split || m.board.Title != "Alpha" {
		t.Error("closing the split keeps the focused board")
	}
}

func TestArrowsCrossSplitBoards(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // crossing saves the focused board as the last one
	t.Setenv("XDG_CONFIG_HOME", "")
	status := SelectField{Name: "Status", Options: []Option{{"Todo", "GRAY"}, {"Doing", "BLUE"}, {"Done", "GREEN"}}}
	snap := func(title string) *Board {
		return &Board{Title: title, Fields: []SelectField{status}, FetchedAt: time.Now(),
			Order: []string{"Todo", "Doing", "Done"}, Totals: map[string]int{}}
	}
	key := func(m model, k string) model {
		t.Helper()
		next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		return next.(model)
	}
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.applyBoard(snap("Alpha"))
	m.openSplit(ProjectRef{Owner: "o", Number: 2})
	m.applyBoard(snap("Beta")) // focused, in slot 1: right of Alpha, or below it

	m.width, m.height = 160, 50 // side by side, 2 columns abreast: Todo Doing / Done
	m = key(m, "h")
	if m.board.Title != "Alpha" || m.col != 1 {
		t.Fatalf("← off Beta's left edge should land on Alpha's right end (Doing), got %s col %d", m.board.Title, m.col)
	}
	m = key(m, "l")
	if m.board.Title != "Beta" || m.col != 0 {
		t.Fatalf("→ off Alpha's right edge should land on Beta's left end, got %s col %d", m.board.Title, m.col)
	}
	m = key(m, "l")
	if m.board.Title != "Beta" || m.col != 1 {
		t.Fatalf("→ inside Beta should move along it, got %s col %d", m.board.Title, m.col)
	}

	m.width = 100 // stacked, 3 columns abreast: Alpha above, Beta below
	m.moveCol(2)
	m = key(m, "k")
	if m.board.Title != "Alpha" || m.col != 2 {
		t.Fatalf("↑ off Beta's top should land on Alpha's bottom row, same place, got %s col %d", m.board.Title, m.col)
	}
	m = key(m, "j")
	if m.board.Title != "Beta" || m.col != 2 {
		t.Fatalf("↓ off Alpha's bottom should land on Beta's top row, got %s col %d", m.board.Title, m.col)
	}
}

func TestTableKeepsTitleStatusAndIteration(t *testing.T) {
	status := SelectField{Name: "Status", Options: []Option{{"Todo", "GRAY"}, {"In Progress", "BLUE"}, {"Done", "GREEN"}}}
	b := &Board{Fields: []SelectField{status}, FetchedAt: time.Now(), Windowed: true, Table: true,
		Order: []string{"No Status", "Todo", "In Progress", "Done"}, Totals: map[string]int{"Todo": 1, "Done": 1}, Rows: 2,
		TableFields: []ViewField{{"Title", "TITLE"}, {"Assignees", "ASSIGNEES"}, {"Status", "SINGLE_SELECT"},
			{"Linked pull requests", "LINKED_PULL_REQUESTS"}, {"Sub-issues progress", "SUB_ISSUES_PROGRESS"},
			{"Release Plan", "ITERATION"}, {"Priority", "SINGLE_SELECT"}},
		Items: []Item{
			{ID: "a", Title: "Scan barcode camera: popup in sync with the cart, without double counting", Number: 1,
				Assignees: []string{"someone"}, LinkedPRs: []int{855}, SubDone: 1, SubTotal: 4,
				Values: map[string]string{"Status": "Todo", "Release Plan": "Release Nov W1", "Priority": "P1"}},
			{ID: "b", Title: "Short one", Number: 2, Values: map[string]string{"Status": "Done", "Release Plan": "Release Oct W5"}},
		}}
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.width, m.height = 160, 30
	m.applyBoard(b)

	for _, w := range []int{160, 90, 50} {
		_, cols, hidden := m.tableColumns(w - 4)
		var names []string
		for _, c := range cols {
			names = append(names, c.f.Name)
		}
		got := strings.Join(names, ",")
		if !strings.Contains(got, "Status") || !strings.Contains(got, "Release Plan") {
			t.Errorf("width %d: Status and Release Plan must stay, got %q", w, got)
		}
		if w == 160 && hidden != 0 {
			t.Errorf("width 160: nothing should be hidden, got %q", got)
		}
		if w == 50 && got != "Status,Release Plan" {
			t.Errorf("width 50: only Status and Release Plan next to Title, got %q", got)
		}
		out := ansi.Strip(m.tableView(w, 12))
		for _, line := range strings.Split(out, "\n") {
			if lipgloss.Width(line) > w {
				t.Errorf("width %d: line overflows: %q", w, line)
			}
		}
		if !strings.Contains(out, "Release Oct W5") && w > 50 {
			t.Errorf("width %d: Release Plan values missing:\n%s", w, out)
		}
		t.Log("\n" + out)
	}
}

func TestSearchSavedPerBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // last.json goes to a scratch config dir
	t.Setenv("XDG_CONFIG_HOME", "")
	board := func() *Board {
		return &Board{FetchedAt: time.Now(), Windowed: true, Order: []string{"Todo"}, Totals: map[string]int{"Todo": 0}}
	}
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.views, m.viewIdx = []ProjectView{{Number: 3, Name: "Bugs"}}, 0
	m.query = "assignee:@me"
	m.rememberBoard()

	// another project, without a search: the first board's search stays saved
	m2 := newModel(nil, "o", 2, time.Minute, "", "")
	m2.saved = m.saved
	m2.rememberBoard()
	last, _ := loadLast()
	if last.Number != 2 || last.Searches["o/1 › Bugs"] != "assignee:@me" {
		t.Fatalf("want board o/2 opened last and o/1's search kept, got %+v", last)
	}

	// reopening o/1 › Bugs: its first sync brings the search back into the box
	m3 := newModel(nil, "o", 1, time.Minute, "", "Bugs")
	m3.saved, m3.restore = last, last.Searches
	m3.fetchSeq = 1
	m3.gotBoard(boardMsg{seq: 1, board: board(), viewIdx: 0, search: "assignee:@me", meta: &ProjectMeta{Views: m.views}})
	if m3.filter.Value() != "assignee:@me" || m3.query != "assignee:@me" || m3.restore != nil {
		t.Fatalf("search not restored: box %q, query %q", m3.filter.Value(), m3.query)
	}

	// clearing it forgets it
	m3.filter.SetValue("")
	m3.query = ""
	m3.rememberBoard()
	if last, _ := loadLast(); len(last.Searches) != 0 {
		t.Fatalf("cleared search should be forgotten, got %v", last.Searches)
	}
}

func TestEachViewKeepsItsSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	m := newModel(nil, "o", 1, time.Minute, "", "")
	m.views = []ProjectView{{Number: 1, Name: "Bug"}, {Number: 2, Name: "Release Plan"}}
	m.viewIdx = 0
	m.filter.SetValue("label:bug assignee:rahmanto1")
	m.query = m.filter.Value()

	m.switchView(1) // Release Plan never had a search
	if m.filter.Value() != "" || m.query != "" {
		t.Fatalf("Release Plan should start without the Bug search, got %q", m.filter.Value())
	}
	m.filter.SetValue("assignee:@me")
	m.query = m.filter.Value()

	m.switchView(0)
	if m.filter.Value() != "label:bug assignee:rahmanto1" {
		t.Fatalf("Bug should get its search back, got %q", m.filter.Value())
	}
	m.switchView(1)
	if m.filter.Value() != "assignee:@me" {
		t.Fatalf("Release Plan should get its search back, got %q", m.filter.Value())
	}
	if last, _ := loadLast(); len(last.Searches) != 2 {
		t.Fatalf("both views' searches should be saved, got %v", last.Searches)
	}
}

func TestOrBranches(t *testing.T) {
	for f, want := range map[string][]string{
		"":                             {""},
		"label:bug is:open":            {"label:bug is:open"},
		"assignee:@me OR no:assignee":  {"assignee:@me", "no:assignee"},
		"assignee:@me | no:assignee":   {"assignee:@me", "no:assignee"},
		"assignee:@me|no:assignee":     {"assignee:@me", "no:assignee"},
		`status:"A|B" OR label:x`:      {`status:"A|B"`, "label:x"},
		"OR label:x OR":                {"label:x"},
		"color or size":                {"color or size"}, // only a capital OR splits
		`title:"this OR that"`:         {`title:"this OR that"`},
		"label:a OR label:b | label:c": {"label:a", "label:b", "label:c"},
	} {
		if got := orBranches(f); strings.Join(got, "‖") != strings.Join(want, "‖") {
			t.Errorf("orBranches(%q) = %q, want %q", f, got, want)
		}
	}
	if got := andFilter("-status:Done", "assignee:@me OR no:assignee"); got != "-status:Done assignee:@me OR -status:Done no:assignee" {
		t.Errorf("andFilter: the view's filter should narrow every branch, got %q", got)
	}
	if got := andFilter("-status:Done", "label:bug"); got != "-status:Done label:bug" {
		t.Errorf("andFilter without OR: got %q", got)
	}
	// a column a branch shows stays
	if !columnShown(`status:"Todo" OR status:"Done"`, "Status", "Done") || columnShown(`status:"Todo" OR status:"Done"`, "Status", "Doing") {
		t.Error("columnShown should show the columns any branch shows, and only those")
	}
}
