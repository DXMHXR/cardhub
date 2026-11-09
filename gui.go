package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// GUI mode (cardhub -gui): Herdr's side panel draws CardHub natively, with the
// "cardhub" Herdr mod. This process still runs everything (the syncs, the AI
// link, the pull requests), in a terminal nobody sees; it publishes what the
// panel shows as gui/state.json whenever that changes, and runs the panel's
// clicks, which land as small JSON files in gui/commands/. Outside Herdr (no
// -gui) CardHub is the terminal app it always was.

// guiCardsPerColumn is how many cards a column loads at first; "Show more"
// adds as many again, up to the 100 a windowed sync can load.
const guiCardsPerColumn = 30

func guiDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cardhub", "gui")
}

// guiOut remembers what was last published, shared by every copy of the model.
type guiOut struct {
	dir  string
	last []byte
	more map[string]int // cards per column the panel asked for, by column name

	screen string      // "issues" while the panel shows its Issues screen (the terminal has none)
	issues issueScreen // the Issues screen
}

// ---- state: what the panel shows ----

type guiState struct {
	PID      int          `json:"pid"`
	Version  string       `json:"version"`
	Screen   string       `json:"screen"` // board or prs
	Project  *guiProject  `json:"project"`
	Views    []guiView    `json:"views"`
	ViewIdx  int          `json:"viewIdx"`
	Search   string       `json:"search"`
	Sync     guiSync      `json:"sync"`
	Link     guiLink      `json:"link"`
	LinkPage guiLinkPage  `json:"linkPage"`
	Board    *guiBoard    `json:"board"`
	Detail   *guiDetail   `json:"detail"`
	PRs      guiPRs       `json:"prs"`
	Issues   guiIssues    `json:"issues"`
	Projects *guiProjects `json:"projects"`
	Repos    *guiRepos    `json:"repos"`
	Update   string       `json:"update,omitempty"` // a newer release, when there is one
}

type guiProject struct {
	Owner  string `json:"owner"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type guiView struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Table bool   `json:"table"`
}

type guiSync struct {
	Fetching  bool   `json:"fetching"`
	Paused    bool   `json:"paused"`
	FetchedAt int64  `json:"fetchedAt"` // unix ms, 0 before the first sync
	NextAt    int64  `json:"nextAt"`    // unix ms of the next sync, 0 when none is due
	Error     string `json:"error,omitempty"`
	Interval  int    `json:"interval"` // seconds: the fastest the user allows (+/-)
	Every     int    `json:"every"`    // seconds between syncs now (stretched to save API budget)
	Remaining int    `json:"remaining"`
	Limit     int    `json:"limit"`
	ResetAt   int64  `json:"resetAt"`
	Low       bool   `json:"low"` // the budget is nearly gone: syncs are throttled
}

// guiLinkPage is the AI link screen: the watchers, their rules, how to connect
// an agent and one activity timeline, as the terminal's screen 4 shows them.
type guiLinkPage struct {
	Watchers []guiWatcher  `json:"watchers"`
	Activity []guiActivity `json:"activity"` // newest first
	Prompt   string        `json:"prompt"`   // the sample prompt to paste into an agent
}

type guiWatcher struct {
	Board    string        `json:"board"` // owner/number
	Title    string        `json:"title"`
	OnScreen bool          `json:"onScreen"`
	Active   bool          `json:"active"`
	Conflict string        `json:"conflict,omitempty"`
	Rules    []guiRuleView `json:"rules"`
	Queued   int           `json:"queued"`
	Sent     int           `json:"sent"`
	Waiting  bool          `json:"waiting"`
}

type guiRuleView struct {
	ID       int    `json:"id"`
	Filter   string `json:"filter"`
	Matches  int    `json:"matches"`
	Reported int    `json:"reported"`
}

type guiActivity struct {
	At    int64     `json:"at"`
	Kind  string    `json:"kind"` // rule | queued | sent | agent | stop
	Text  string    `json:"text"`
	Board string    `json:"board"`
	Card  *guiEvent `json:"card,omitempty"`
}

type guiEvent struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Color  string `json:"color"`
	Rule   int    `json:"rule"`
}

// guiActivityMax is how much of the timeline the panel gets.
const guiActivityMax = 60

// guiLink is the AI link footer: what the terminal's AI link box says.
type guiLink struct {
	Boards   int    `json:"boards"`
	Rules    int    `json:"rules"`
	Sent     int    `json:"sent"`
	Queued   int    `json:"queued"`
	Waiting  int    `json:"waiting"`
	Conflict string `json:"conflict,omitempty"`
}

type guiBoard struct {
	Group   string      `json:"group"`
	Table   bool        `json:"table"`
	Columns []guiColumn `json:"columns"`
	Rows    int         `json:"rows"`  // a table view: all its rows
	Items   []guiCard   `json:"items"` // a table view: the loaded ones
	Fields  []string    `json:"fields"`
	Flows   []guiFlow   `json:"flows"` // connector k joins columns k and k+1
}

// guiFlow is a connector's traffic this session: like the terminal's, a lane a
// card crossed streams a white dot in the latest move's direction.
type guiFlow struct {
	Moves int `json:"moves"`
	Dir   int `json:"dir"` // +1 forward, -1 back
}

type guiColumn struct {
	Name  string    `json:"name"`
	Color string    `json:"color"` // #rrggbb
	Total int       `json:"total"`
	Cards []guiCard `json:"cards"`
	More  bool      `json:"more"` // more cards than loaded, and room to load them
}

type guiCard struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"`
	Title     string      `json:"title"`
	Ref       string      `json:"ref"`
	URL       string      `json:"url"`
	State     string      `json:"state"`
	Assignees []string    `json:"assignees"`
	Labels    []string    `json:"labels"`
	Updated   int64       `json:"updated"`
	Fields    [][2]string `json:"fields"`
	Changed   bool        `json:"changed"`         // moved or changed on the last syncs
	Cells     []string    `json:"cells,omitempty"` // a table view's row: one per board field
}

type guiDetail struct {
	Card     guiCard      `json:"card"`
	Color    string       `json:"color"`
	PR       *guiPR       `json:"pr"`
	Loading  bool         `json:"loading"`
	Error    string       `json:"error,omitempty"`
	Author   string       `json:"author"`
	Created  int64        `json:"created"`
	Body     string       `json:"body"`
	Comments []guiComment `json:"comments"`
	Total    int          `json:"total"`
}

type guiComment struct {
	Author  string `json:"author"`
	Created int64  `json:"created"`
	Body    string `json:"body"`
}

type guiPR struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Draft     bool     `json:"draft"`
	Author    string   `json:"author"`
	Head      string   `json:"head"`
	Base      string   `json:"base"`
	Labels    []string `json:"labels"`
	Reviews   []string `json:"reviews"`
	Review    string   `json:"review"`
	Mergeable string   `json:"mergeable"`
	Checks    string   `json:"checks"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Files     int      `json:"files"`
	Comments  int      `json:"comments"`
	Created   int64    `json:"created"`
	Updated   int64    `json:"updated"`
	Merged    int64    `json:"merged"`
	Closed    int64    `json:"closed"`
	MergedBy  string   `json:"mergedBy"`
	NoMerge   string   `json:"noMerge"` // why it can't be merged from here, "" when it can
}

type guiPRs struct {
	Repo      string      `json:"repo"`
	Closed    bool        `json:"closed"` // showing closed ones
	Loading   bool        `json:"loading"`
	Error     string      `json:"error,omitempty"`
	List      []guiPR     `json:"list"`
	Open      int         `json:"open"`
	ClosedN   int         `json:"closedCount"`
	Methods   []guiMethod `json:"methods"`
	FetchedAt int64       `json:"fetchedAt"`
	Flash     string      `json:"flash,omitempty"`
	Merging   int         `json:"merging"` // the pull request being merged
	MergeErr  string      `json:"mergeError,omitempty"`
	MergeOf   int         `json:"mergeErrorOf,omitempty"`
}

type guiIssues struct {
	Closed    bool       `json:"closed"`
	Loading   bool       `json:"loading"`
	Error     string     `json:"error,omitempty"`
	List      []guiIssue `json:"list"`
	Open      int        `json:"open"`
	ClosedN   int        `json:"closedCount"`
	FetchedAt int64      `json:"fetchedAt"`
}

type guiIssue struct {
	Number    int      `json:"number"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Reason    string   `json:"reason"`
	Author    string   `json:"author"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	Milestone string   `json:"milestone"`
	Comments  int      `json:"comments"`
	Created   int64    `json:"created"`
	Updated   int64    `json:"updated"`
	Closed    int64    `json:"closedAt"`
}

type guiMethod struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type guiProjects struct {
	Loading bool         `json:"loading"`
	Error   string       `json:"error,omitempty"`
	List    []guiProject `json:"list"`
}

type guiRepos struct {
	Loading bool      `json:"loading"`
	Error   string    `json:"error,omitempty"`
	List    []guiRepo `json:"list"`
}

type guiRepo struct {
	Repo    string `json:"repo"`
	Project bool   `json:"project"` // one of the project's: recommended
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func colorHex(name string) string {
	if h, ok := ghColors[name]; ok {
		return h
	}
	return ghColors["GRAY"]
}

func (m model) guiCard(it Item) guiCard {
	c := guiCard{ID: it.ID, Kind: it.Kind, Title: it.Title, Ref: it.Ref(), URL: it.URL, State: it.State,
		Assignees: it.Assignees, Labels: it.Labels, Updated: ms(it.UpdatedAt)}
	if t, ok := m.changedAt[it.ID]; ok && time.Since(t) < flashFor {
		c.Changed = true
	}
	keys := make([]string, 0, len(it.Values))
	for k := range it.Values {
		if k != "Title" && k != "Assignees" && k != "Labels" && it.Values[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Fields = append(c.Fields, [2]string{k, it.Values[k]})
	}
	for _, kv := range [][2]string{{"Milestone", it.Milestone}, {"Parent", it.Parent}} {
		if kv[1] != "" {
			c.Fields = append(c.Fields, kv)
		}
	}
	if it.SubTotal > 0 {
		c.Fields = append(c.Fields, [2]string{"Sub-issues", fmt.Sprintf("%d/%d done", it.SubDone, it.SubTotal)})
	}
	return c
}

func (m model) guiPR(p PR) guiPR {
	return guiPR{ID: p.ID, URL: p.URL, Number: p.Number, Title: p.Title, State: p.State, Draft: p.Draft,
		Author: p.Author, Head: p.Head, Base: p.Base, Labels: p.Labels, Reviews: p.Reviews, Review: p.ReviewDecision,
		Mergeable: p.Mergeable, Checks: p.Checks, Additions: p.Additions, Deletions: p.Deletions, Files: p.Files,
		Comments: p.Comments, Created: ms(p.CreatedAt), Updated: ms(p.UpdatedAt), Merged: ms(p.MergedAt),
		Closed: ms(p.ClosedAt), MergedBy: p.MergedBy, NoMerge: m.whyNoMerge(p)}
}

func (m model) guiState() guiState {
	s := guiState{PID: os.Getpid(), Version: versionLabel(), Screen: "board", ViewIdx: m.viewIdx,
		Search: m.filter.Value()}
	switch m.view {
	case viewPRs:
		s.Screen = "prs"
	case viewLink:
		s.Screen = "link"
	}
	if m.gui != nil && m.gui.screen == "issues" {
		s.Screen = "issues"
	}
	if m.updateAvailable() {
		s.Update = m.latest
	}
	if m.owner != "" {
		s.Project = &guiProject{Owner: m.owner, Number: m.number,
			URL: fmt.Sprintf("https://github.com/%s/projects/%d", m.owner, m.number)}
		if m.board != nil {
			s.Project.Title, s.Project.URL = m.board.Title, firstNonEmpty(m.board.URL, s.Project.URL)
		}
	}
	for i, v := range m.views {
		s.Views = append(s.Views, guiView{Index: i, Name: v.Name, Table: v.Layout == "TABLE_LAYOUT"})
	}
	s.Sync = guiSync{Fetching: m.fetching, Paused: m.paused, Error: errText(m.lastErr),
		Interval: int(m.interval.Seconds()), Every: int(m.effectiveInterval().Seconds())}
	if !m.paused && !m.fetching {
		s.Sync.NextAt = ms(m.nextFetch)
	}
	st := m.hub.Status()
	s.Link = guiLink{Boards: st.Boards, Rules: st.Rules, Sent: st.Sent, Queued: st.Queued, Waiting: st.Waiting}
	if m.owner != "" {
		s.Link.Conflict = st.Conflict
	}
	s.LinkPage = m.guiLinkPage()
	if b := m.board; b != nil {
		s.Sync.FetchedAt = ms(b.FetchedAt)
		rl := b.RateLimit
		s.Sync.Remaining, s.Sync.Limit, s.Sync.ResetAt, s.Sync.Low = rl.Remaining, rl.Limit, ms(rl.ResetAt), rl.Limit > 0 && rl.Remaining < lowRateLimit
		gb := &guiBoard{Group: m.groupField, Table: b.Table, Rows: b.Rows, Columns: []guiColumn{}}
		if b.Table {
			for _, f := range b.TableFields {
				gb.Fields = append(gb.Fields, f.Name)
			}
			for _, it := range b.Items {
				c := m.guiCard(it)
				for _, f := range b.TableFields {
					c.Cells = append(c.Cells, m.cellText(it, f))
				}
				gb.Items = append(gb.Items, c)
			}
		}
		for _, c := range m.columns {
			gc := guiColumn{Name: c.name, Color: colorHex(c.color), Total: c.total, Cards: []guiCard{}}
			for _, it := range c.items {
				gc.Cards = append(gc.Cards, m.guiCard(it))
			}
			gc.More = b.Windowed && len(c.items) < min(c.total, 100)
			gb.Columns = append(gb.Columns, gc)
		}
		for k := 0; k+1 < len(m.columns); k++ {
			n, dir := m.traffic(k)
			gb.Flows = append(gb.Flows, guiFlow{Moves: n, Dir: dir})
		}
		s.Board = gb
	}
	if d := m.detail; d.open {
		gd := &guiDetail{Card: m.guiCard(d.item), Color: string(d.color), Loading: d.loading, Error: errText(d.err)}
		if d.pr != nil {
			p := m.guiPR(*d.pr)
			gd.PR = &p
		}
		if d.data != nil {
			gd.Author, gd.Created, gd.Body, gd.Total = d.data.Author, ms(d.data.CreatedAt), plainMarkdown(d.data.Body), d.data.Total
			for _, c := range d.data.Comments {
				gd.Comments = append(gd.Comments, guiComment{Author: c.Author, Created: ms(c.CreatedAt), Body: plainMarkdown(c.Body)})
			}
		}
		s.Detail = gd
	}
	ps := m.prs
	s.PRs = guiPRs{Repo: ps.repo, Closed: ps.closed, Loading: ps.loading, Error: errText(ps.err), List: []guiPR{}}
	if ps.data != nil {
		s.PRs.Open, s.PRs.ClosedN, s.PRs.FetchedAt = ps.data.Open, ps.data.Closed, ms(ps.data.FetchedAt)
		if ps.shown == ps.closed {
			for _, p := range ps.data.PRs {
				s.PRs.List = append(s.PRs.List, m.guiPR(p))
			}
		}
		for _, mm := range ps.data.Methods() {
			s.PRs.Methods = append(s.PRs.Methods, guiMethod{ID: string(mm), Label: mm.Label()})
		}
	}
	if m.gui != nil {
		is := m.gui.issues
		s.Issues = guiIssues{Closed: is.closed, Loading: is.loading, Error: errText(is.err), List: []guiIssue{}}
		if d := is.data; d != nil && strings.EqualFold(d.Repo, ps.repo) {
			s.Issues.Open, s.Issues.ClosedN, s.Issues.FetchedAt = d.Open, d.Closed, ms(d.FetchedAt)
			if is.shown == is.closed {
				for _, x := range d.Issues {
					s.Issues.List = append(s.Issues.List, guiIssue{Number: x.Number, URL: x.URL, Title: x.Title, State: x.State,
						Reason: x.Reason, Author: x.Author, Labels: x.Labels, Assignees: x.Assignees, Milestone: x.Milestone,
						Comments: x.Comments, Created: ms(x.CreatedAt), Updated: ms(x.UpdatedAt), Closed: ms(x.ClosedAt)})
				}
			}
		}
	}
	if ps.flash != "" && time.Since(ps.flashAt) < 8*time.Second {
		s.PRs.Flash = ps.flash
	}
	if ps.merge.open {
		if ps.merge.merging {
			s.PRs.Merging = ps.merge.pr.Number
		}
		if ps.merge.err != nil {
			s.PRs.MergeErr, s.PRs.MergeOf = ps.merge.err.Error(), ps.merge.pr.Number
		}
	}
	if m.picker.list != nil || m.picker.loading || m.picker.err != nil {
		gp := &guiProjects{Loading: m.picker.loading, Error: errText(m.picker.err), List: []guiProject{}}
		for _, p := range m.picker.list {
			gp.List = append(gp.List, guiProject{Owner: p.Owner, Number: p.Number, Title: p.Title})
		}
		s.Projects = gp
	}
	if ps.repos != nil || ps.reposLoading || ps.reposErr != nil {
		gr := &guiRepos{Loading: ps.reposLoading, Error: errText(ps.reposErr), List: []guiRepo{}}
		for _, r := range m.repoRows() {
			gr.List = append(gr.List, guiRepo{Repo: r.repo, Project: strings.HasPrefix(r.meta, "★")})
		}
		s.Repos = gr
	}
	return s
}

func (m model) guiLinkPage() guiLinkPage {
	pg := guiLinkPage{Watchers: []guiWatcher{}, Activity: []guiActivity{}, Prompt: samplePrompt()}
	snaps := m.hub.Snapshots()
	for _, sn := range snaps {
		w := guiWatcher{Board: sn.Board, Title: sn.Title, OnScreen: sn.OnScreen, Active: sn.Active, Conflict: sn.Conflict,
			Rules: []guiRuleView{}, Queued: sn.Queued, Sent: sn.Sent, Waiting: sn.Waiting}
		for _, r := range sn.Rules {
			w.Rules = append(w.Rules, guiRuleView{ID: r.ID, Filter: r.Filter, Matches: r.Matches, Reported: r.Reported})
		}
		pg.Watchers = append(pg.Watchers, w)
		for _, l := range sn.Log {
			a := guiActivity{At: ms(l.At), Kind: l.Kind, Text: l.Text, Board: firstNonEmpty(sn.Title, sn.Board)}
			if c := l.Card; c != nil {
				a.Card = &guiEvent{Number: c.Number, Title: c.Title, Status: c.Status, Color: colorHex(m.columnColor(c.Status)), Rule: c.Rule}
			}
			pg.Activity = append(pg.Activity, a)
		}
	}
	sort.SliceStable(pg.Activity, func(i, j int) bool { return pg.Activity[i].At > pg.Activity[j].At })
	if len(pg.Activity) > guiActivityMax {
		pg.Activity = pg.Activity[:guiActivityMax]
	}
	return pg
}

// publish writes state.json when what the panel shows changed. The panel
// watches the folder, so it redraws at once.
func (m model) publish() {
	g := m.gui
	if g == nil || g.dir == "" {
		return
	}
	b, err := json.Marshal(m.guiState())
	if err != nil || string(b) == string(g.last) {
		return
	}
	g.last = b
	_ = os.MkdirAll(g.dir, 0o755)
	tmp := filepath.Join(g.dir, ".state.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(g.dir, "state.json"))
	}
}

// ---- commands: the panel's clicks ----

type guiCmd struct {
	Cmd     string `json:"cmd"`
	Screen  string `json:"screen"`
	Index   int    `json:"index"`
	Text    string `json:"text"`
	Column  string `json:"column"`
	ID      string `json:"id"`
	Number  int    `json:"number"`
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Closed  bool   `json:"closed"`
	Method  string `json:"method"`
	Created int64  `json:"at"` // unix ms the panel sent it
}

// watchGUICommands hands the panel's clicks to p as they land, oldest first.
func watchGUICommands(p *tea.Program, dir string) {
	cmds := filepath.Join(dir, "commands")
	_ = os.MkdirAll(cmds, 0o755)
	for {
		entries, _ := os.ReadDir(cmds)
		names := []string{}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names) // named <unix ms>-<n>.json
		for _, n := range names {
			path := filepath.Join(cmds, n)
			b, err := os.ReadFile(path)
			_ = os.Remove(path)
			var c guiCmd
			if err != nil || json.Unmarshal(b, &c) != nil {
				continue
			}
			if c.Created > 0 && time.Since(time.UnixMilli(c.Created)) > time.Minute {
				continue // a click from before a restart: stale
			}
			p.Send(c)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// runGUI does one of the panel's clicks.
func (m *model) runGUI(c guiCmd) tea.Cmd {
	switch c.Cmd {
	case "screen":
		m.gui.screen = ""
		switch c.Screen {
		case "issues": // the repo is the Pull requests screen's
			m.gui.screen = "issues"
			if m.prs.repo == "" && m.saved.PRRepos[m.projectKey()] != "" {
				m.prs.repo = m.saved.PRRepos[m.projectKey()]
			}
			if m.issuesDue() {
				return m.fetchIssues()
			}
			return nil
		case "prs":
			return m.showView(viewPRs)
		case "link":
			return m.showView(viewLink)
		}
		return m.showView(viewBoard)
	case "stopWatcher": // the panel asked first, as the terminal's X does
		m.hub.StopWatcher(c.Text)
	case "view":
		m.detail.open = false
		return m.switchView(c.Index)
	case "search":
		m.filter.SetValue(c.Text)
		return m.applySearch()
	case "refresh":
		if m.gui.screen == "issues" {
			return m.fetchIssues()
		}
		if m.view == viewPRs {
			return m.fetchPRs()
		}
		if m.fetching {
			return nil
		}
		return m.fetchCmd()
	case "interval": // the panel's − / +, like the terminal's - / +
		d := time.Duration(c.Index) * 5 * time.Second
		if m.interval+d >= 5*time.Second {
			m.interval += d
			m.nextFetch = m.nextFetch.Add(d)
		}
	case "pause": // the panel asks before pausing, as the terminal does
		if m.paused = !m.paused; !m.paused && !m.fetching {
			return m.fetchCmd() // resume with a fresh sync
		}
	case "more":
		g := m.gui
		if g.more == nil {
			g.more = map[string]int{}
		}
		g.more[c.Column] = min(100, max(g.more[c.Column], guiCardsPerColumn)+guiCardsPerColumn)
		if m.isTable() {
			m.tableOff = min(m.tableOff+guiCardsPerColumn, 100)
		}
		return m.fetchCmd()
	case "card":
		if m.board == nil {
			return nil
		}
		for _, col := range m.columns {
			for _, it := range col.items {
				if it.ID == c.ID {
					return m.openDetailOn(it, lipgloss.Color(colorHex(col.color)), nil)
				}
			}
		}
		for _, it := range m.board.Items { // a table view's rows
			if it.ID == c.ID {
				return m.openDetailOn(it, lipgloss.Color(colorHex("GRAY")), nil)
			}
		}
	case "pr":
		if m.prs.data != nil {
			for _, p := range m.prs.data.PRs {
				if p.Number == c.Number {
					return m.openPRDetail(p)
				}
			}
		}
	case "close":
		m.detail.open = false
		if !m.prs.merge.merging {
			m.prs.merge = mergeDialog{}
		}
	case "issueState":
		if c.Closed != m.gui.issues.closed {
			m.gui.issues.closed = c.Closed
			return m.fetchIssues()
		}
	case "issue":
		if d := m.gui.issues.data; d != nil {
			for _, x := range d.Issues {
				if x.Number == c.Number {
					color := ghColors["GREEN"]
					if x.State == "CLOSED" {
						color = ghColors["PURPLE"]
					}
					return m.openDetailOn(issueItem(x, d.Repo), lipgloss.Color(color), nil)
				}
			}
		}
	case "prState":
		if c.Closed != m.prs.closed {
			m.prs.closed, m.prs.row, m.prs.off = c.Closed, 0, 0
			return m.fetchPRs()
		}
	case "merge":
		return m.guiMerge(c.Number, MergeMethod(c.Method))
	case "projects":
		if !m.picker.loading {
			m.picker.loading, m.picker.err = true, nil
			return listProjects(m.client)
		}
	case "project":
		m.detail.open = false
		m.picker.open = false
		return m.switchProject(ProjectRef{Owner: c.Owner, Number: c.Number})
	case "repos":
		return m.loadRepoLists()
	case "repo":
		cmd := m.switchRepo(c.Repo)
		if m.gui.screen == "issues" { // the Issues screen follows the repo too
			return tea.Batch(cmd, m.fetchIssues())
		}
		return cmd
	}
	return nil
}

// guiMerge merges pull request n of the shown list with method: the panel
// asked which way and confirmed it already.
func (m *model) guiMerge(n int, method MergeMethod) tea.Cmd {
	if m.prs.merge.merging || m.prs.data == nil {
		return nil
	}
	for _, p := range m.prs.data.PRs {
		if p.Number != n {
			continue
		}
		m.prs.merge = mergeDialog{open: true, pr: p, methods: m.prs.data.Methods()}
		if why := m.whyNoMerge(p); why != "" {
			m.prs.merge.err = fmt.Errorf("%s", why)
			return nil
		}
		ok := false
		for _, mm := range m.prs.merge.methods {
			ok = ok || mm == method
		}
		if !ok {
			m.prs.merge.err = fmt.Errorf("%s doesn't allow %s", m.prs.data.Repo, strings.ToLower(method.Label()))
			return nil
		}
		m.prs.merge.merging = true
		c, id := m.client, p.ID
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return mergedMsg{pr: p, method: method, err: c.MergePR(ctx, id, method)}
		}
	}
	return nil
}

// ---- wiring ----

// Update runs the model and, in GUI mode, publishes what changed. Ticks of
// the terminal's own animations don't change the panel; the once-a-second
// tick republishes (ages, flashes running out).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if c, ok := msg.(guiCmd); ok {
		cmd := m.runGUI(c)
		m.publish()
		return m, cmd
	}
	if msg, ok := msg.(issuesMsg); ok {
		m.gotIssues(msg)
		m.publish()
		return m, nil
	}
	next, cmd := m.update(msg)
	if m2, ok := next.(model); ok && m2.gui != nil {
		switch msg.(type) {
		case animMsg, spinner.TickMsg:
		case tickMsg: // the Issues screen refreshes on the board's cadence too
			if m2.gui.screen == "issues" && !m2.paused && m2.issuesDue() {
				cmd = tea.Batch(cmd, m2.fetchIssues())
			}
			m2.publish()
		default:
			m2.publish()
		}
		return m2, cmd
	}
	return next, cmd
}
