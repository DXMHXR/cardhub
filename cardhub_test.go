package main

import (
	"fmt"
	"github.com/charmbracelet/lipgloss"
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

// The fish stays inside its bowl: the glass (columns 2 and 19) is never drawn over.
func TestFishStaysInBowl(t *testing.T) {
	for step := 0; step < 60; step++ {
		for _, md := range []mood{{}, {awake: true, since: 9}} {
			p := scenes[3](step, md)
			for y := 4; y < 12; y++ {
				if p[y][2] != 'a' || p[y][19] != 'a' {
					t.Fatalf("step %d mood %+v: glass at row %d is %q/%q", step, md, y, p[y][2], p[y][19])
				}
			}
		}
	}
}

// TestScenes plays every empty-column scene for a while (go test -run
// TestScenes -v shows a few steps of each as pixel codes) and checks every
// pixel uses a known palette code.
func TestScenes(t *testing.T) {
	known := palette("BLUE", true)
	for i, sc := range scenes {
		var shots []string
		for step := 0; step < 60; step++ {
			md := mood{awake: step >= 30, since: step - 30} // idle, then woken up
			p := sc(step, md)
			var rows []string
			for _, row := range p {
				var b strings.Builder
				for _, px := range row {
					if px == 0 {
						b.WriteByte('.')
						continue
					}
					if _, ok := known[px]; !ok {
						t.Fatalf("scene %d step %d: unknown pixel code %q", i, step, px)
					}
					b.WriteByte(px)
				}
				rows = append(rows, b.String())
			}
			if step%7 == 0 && step < 21 {
				shots = append(shots, fmt.Sprintf("step %d:\n%s", step, strings.Join(rows, "\n")))
			}
		}
		t.Logf("scene %d\n%s", i, strings.Join(shots, "\n"))
	}
}

// Empty-column animations are off unless the user turned them on.
func TestPetsOffByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if loadSettings().Pets {
		t.Fatal("with no settings file, animations should be off")
	}
	saveSettings(settings{Pets: true})
	if !loadSettings().Pets {
		t.Fatal("turning animations on should be saved")
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

// TestPetsGallery writes animation frames of every pet, in a range of column
// colours, for the README (CARDHUB_GALLERY=<dir> go test -run TestPetsGallery).
func TestPetsGallery(t *testing.T) {
	dir := os.Getenv("CARDHUB_GALLERY")
	if dir == "" {
		t.Skip("set CARDHUB_GALLERY to a folder to write the frames")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	colors := []string{"GREEN", "YELLOW", "ORANGE", "BLUE", "GREEN", "PINK", "PURPLE", "RED"}
	for step := 0; step < 48; step++ {
		var out []string
		for row := 0; row < 2; row++ {
			var cells []string
			for i := row * 4; i < row*4+4; i++ {
				md := mood{}
				if step >= 32 && i%2 == 0 { // every other pet wakes up near the end
					md = mood{awake: true, since: step - 32}
				}
				cells = append(cells, strings.Join(scenes[i](step, md).render(palette(colors[i], true)), "\n"), "  ")
			}
			out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, cells...), "")
		}
		name := filepath.Join(dir, fmt.Sprintf("f%02d.ansi", step))
		if err := os.WriteFile(name, []byte(strings.Join(out, "\n")), 0o644); err != nil {
			t.Fatal(err)
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
