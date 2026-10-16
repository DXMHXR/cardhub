package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type view int

const (
	viewBoard view = iota
	viewStats
	viewLink // the AI link screen
	numViews
)

const (
	historyLen   = 60
	flashFor     = 90 * time.Second
	lowRateLimit = 500
)

// ---- messages ----

type boardMsg struct {
	board   *Board
	err     error
	seq     int
	meta    *ProjectMeta // set when project metadata was (re)loaded with this fetch
	viewIdx int
	group   string // field the snapshot is grouped by
}

type clearCopiedMsg struct{}

// linkMsg: the AI link changed (agent connected, rule added…); re-render, and
// check rules right away when one was just added.
type linkMsg struct{ added bool }

// rulesMsg carries, per watcher, the cards currently matching each AI rule.
type rulesMsg struct {
	results []ruleResult
	at      time.Time
}

type ruleResult struct {
	w            *Notifier
	matches      map[int][]Item
	title, group string // set when the project's metadata was loaded for this check
	rl           RateLimit
	err          error
}

// filterMsg fires after the user pauses typing in the "/" search, so the
// server-side search isn't re-run on every keystroke.
type filterMsg struct{ seq int }
type tickMsg time.Time

// ---- model ----

type column struct {
	name  string
	color string
	items []Item // loaded cards (on a windowed board, only the first few)
	total int    // real number of cards in the column
}

type model struct {
	client   *Client
	owner    string
	number   int
	interval time.Duration

	board      *Board
	meta       *ProjectMeta
	views      []ProjectView // meta.Views
	viewIdx    int           // index into views; -1 = all items
	viewArg    string        // -view flag, resolved once views are loaded
	resetDiff  bool          // next snapshot starts fresh (view changed)
	moves      []move        // recent column moves, lighting up the connectors they crossed
	groupField string
	columns    []column
	history    []map[string]int // column counts per poll, oldest first
	changedAt  map[string]time.Time

	view           view
	col, row       int            // selected column, and the cursor within it
	cursors        map[string]int // remembered cursor per column name
	offsets        map[string]int // first visible card per column name
	settings       settings       // saved between runs (Settings menu, S)
	settingsOpen   bool
	settingsCursor int

	frame      int // animation frame for the flow connectors and pets
	focusFrame int // frame the selected column got the cursor (pets wake up)

	filter    textinput.Model
	filtering bool
	filterSeq int // debounce counter for the "/" search
	saved     lastState

	hub          *Hub // AI watchers, one per project; nil when not running interactively
	nextRules    time.Time
	ruleChecking bool
	recheck      bool        // a rule was added while a check was running
	copied       string      // confirmation after copying the sample prompt
	paused       bool        // Space: the board stops refreshing (AI watchers keep running)
	confirm      confirmKind // an open "Pause syncing?" / "Quit CardHub?" / "Stop watching?" dialog
	stopTarget   string      // the watcher (owner/number) the stop dialog is about
	query        string      // "/" search text the current board was fetched with
	picker       picker

	fetching  bool
	fetchSeq  int
	nextFetch time.Time
	lastErr   error
	spin      spinner.Model

	width, height int
}

func newModel(c *Client, owner string, number int, interval time.Duration, group, viewArg string) model {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search titles, or filters like label:bug assignee:@me is:open"
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	m := model{
		client: c, owner: owner, number: number, interval: interval,
		groupField: group, viewArg: viewArg, viewIdx: -1, changedAt: map[string]time.Time{},
		cursors: map[string]int{}, offsets: map[string]int{}, focusFrame: -1000,
		settings: loadSettings(),
		filter:   ti, spin: sp,
	}
	if owner == "" { // nothing to show yet: open on the project picker
		m.picker = picker{open: true, kind: pickProject, loading: true}
	}
	return m
}

func (m model) Init() tea.Cmd {
	if m.owner == "" {
		return tea.Batch(m.spin.Tick, tick(), animate(), listProjects(m.client))
	}
	return tea.Batch(m.spin.Tick, tick(), animate(), func() tea.Msg { return startFetch{} })
}

type startFetch struct{}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// metaEvery re-reads project metadata (columns, views) every this many syncs,
// so a newly added Status option or view shows up without a restart.
const metaEvery = 20

func (m *model) fetchCmd() tea.Cmd {
	m.fetching = true
	m.fetchSeq++
	seq, c, owner, num := m.fetchSeq, m.client, m.owner, m.number
	meta, idx, arg, group := m.meta, m.viewIdx, m.viewArg, m.groupField
	if seq%metaEvery == 0 {
		meta = nil
	}
	// the stats screen needs every card; the board only the ones on screen
	full := m.view == viewStats
	search := strings.TrimSpace(m.filter.Value())
	m.query = search
	limits := m.cardLimits()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		msg := boardMsg{seq: seq, viewIdx: idx}
		if meta == nil {
			md, err := c.FetchMeta(ctx, owner, num)
			if err != nil {
				msg.err = err
				return msg
			}
			meta, msg.meta = md, md
			if arg != "" && idx < 0 {
				if msg.viewIdx = FindView(md.Views, arg); msg.viewIdx < 0 {
					msg.err = fmt.Errorf("no view %q in this project", arg)
					return msg
				}
			}
		}
		if msg.viewIdx >= len(meta.Views) {
			msg.viewIdx = -1
		}
		filter := search
		if msg.viewIdx >= 0 {
			v := meta.Views[msg.viewIdx]
			filter = strings.TrimSpace(v.Filter + " " + search)
			if group == "" {
				group = v.GroupBy
			}
		}
		msg.group = resolveGroup(meta.Fields, group)
		if full {
			msg.board, msg.err = c.FetchBoard(ctx, owner, num, meta, filter)
		} else {
			msg.board, msg.err = c.FetchColumns(ctx, owner, num, meta, msg.group, filter, limits)
		}
		return msg
	}
}

// resolveGroup picks the grouping field: the wanted one if it exists, else Status,
// else the first single-select field.
func resolveGroup(fields []SelectField, want string) string {
	for _, f := range fields {
		if f.Name == want {
			return want
		}
	}
	for _, f := range fields {
		if strings.EqualFold(f.Name, "Status") {
			return f.Name
		}
	}
	if len(fields) > 0 {
		return fields[0].Name
	}
	return "Status"
}

// cardLimits is how many cards to load per column on the next windowed sync:
// what's on screen plus one more window of read-ahead for scrolling.
func (m model) cardLimits() map[string]int {
	lim := map[string]int{}
	for _, c := range m.columns {
		lim[c.name] = min(100, m.offsets[c.name]+2*visibleCards)
	}
	return lim
}

// checkRules evaluates every watcher's rules: one request per watched project
// (plus, the first time, one to learn an off-screen project's title and status
// field). It runs on its own timer, independent of the project on screen.
func (m *model) checkRules() tea.Cmd {
	if m.hub == nil || m.ruleChecking {
		return nil
	}
	type job struct {
		w        *Notifier
		filters  map[int]string
		needMeta bool
	}
	var jobs []job
	for _, w := range m.hub.All() {
		if f := w.Filters(); len(f) > 0 && w.Status().Active {
			_, group := w.Meta()
			jobs = append(jobs, job{w, f, group == ""})
		}
	}
	if len(jobs) == 0 {
		return nil
	}
	m.ruleChecking = true
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		msg := rulesMsg{at: time.Now()}
		for _, j := range jobs {
			r := ruleResult{w: j.w}
			if j.needMeta {
				if meta, err := c.FetchMeta(ctx, j.w.owner, j.w.number); err == nil {
					r.title, r.group = meta.Title, resolveGroup(meta.Fields, "")
				}
			}
			r.matches, r.rl, r.err = c.FetchMatches(ctx, j.w.owner, j.w.number, j.filters)
			msg.results = append(msg.results, r)
		}
		return msg
	}
}

// rememberBoard saves the open project and view so plain `cardhub` reopens them.
func (m *model) rememberBoard() {
	s := lastState{Owner: m.owner, Number: m.number}
	if v := m.currentView(); v != nil {
		s.View = v.Name
	}
	if s != m.saved {
		saveLast(s)
		m.saved = s
	}
}

// applySearch re-syncs with the "/" search text when it changed. The search runs
// on GitHub (it accepts free text and filters like label:bug), so column totals
// stay exact. A new search starts a fresh snapshot: cards dropping out of the
// results aren't moves.
func (m *model) applySearch() tea.Cmd {
	if strings.TrimSpace(m.filter.Value()) == m.query {
		return nil
	}
	m.resetDiff = true
	m.resetCursors()
	return m.fetchCmd() // supersedes any in-flight sync
}

// statsNeedsAll re-syncs when the screen changes to one wanting the other fetch:
// the stats screen loads every card, the board goes back to the cheap windowed
// sync (and its shorter auto interval) straight away.
func (m *model) statsNeedsAll() tea.Cmd {
	// only the stats screen needs every card; the board and AI link screens use the cheap sync
	if m.board == nil || m.board.Windowed == (m.view != viewStats) {
		return nil
	}
	return m.fetchCmd()
}

// needsMore reports whether the selected column is scrolled past its loaded cards.
func (m model) needsMore() bool {
	if m.board == nil || !m.board.Windowed || m.col >= len(m.columns) {
		return false
	}
	c := m.columns[m.col]
	end := min(m.offsets[c.name]+visibleCards, c.total, 100)
	return len(c.items) < end
}

// loadMore syncs right away when scrolling reached cards that aren't loaded yet.
func (m *model) loadMore() tea.Cmd {
	if m.fetching || !m.needsMore() {
		return nil
	}
	return m.fetchCmd()
}

// effectiveInterval is the user's interval, stretched automatically for
// expensive boards and when the shared API budget is running low.
func (m model) effectiveInterval() time.Duration {
	if m.board == nil {
		return m.interval
	}
	return autoInterval(m.interval, m.board.Cost, m.board.RateLimit, time.Now())
}

const (
	budgetShare = 0.15            // spend at most this share of the hourly limit
	maxInterval = 5 * time.Minute // never refresh less often than this
)

// autoInterval picks how long to wait between syncs that each cost `cost` points.
// base is the fastest allowed. It slows down so that cardhub (a) uses at most
// budgetShare of the hourly limit and (b) uses at most half of what's left
// before the limit resets, so other tools sharing the token keep working.
func autoInterval(base time.Duration, cost int, rl RateLimit, now time.Time) time.Duration {
	cost = max(cost, 1)
	d := base
	if rl.Limit > 0 {
		d = max(d, time.Duration(float64(time.Hour)*float64(cost)/(budgetShare*float64(rl.Limit))))
	}
	if left := rl.ResetAt.Sub(now); left > 0 {
		if rl.Remaining <= 0 {
			d = max(d, left)
		} else {
			d = max(d, time.Duration(float64(left)*float64(cost)/(0.5*float64(rl.Remaining))))
		}
	}
	if rl.Remaining < lowRateLimit {
		d = max(d, 2*time.Minute)
	}
	d = min(max(d, base), max(base, maxInterval))
	return d.Round(5 * time.Second)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case animMsg:
		m.frame++
		return m, animate()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case startFetch:
		if m.fetching {
			return m, nil
		}
		return m, m.fetchCmd()

	case tickMsg:
		var cmds []tea.Cmd
		if !m.paused && !m.fetching && !m.nextFetch.IsZero() && time.Now().After(m.nextFetch) {
			cmds = append(cmds, m.fetchCmd())
		}
		if time.Now().After(m.nextRules) { // AI watchers have their own timer, unaffected by pause
			cmds = append(cmds, m.checkRules())
		}
		m.hub.Prune(idleAfter)
		if len(cmds) > 0 {
			return m, tea.Batch(append(cmds, tick())...)
		}
		return m, tick()

	case boardMsg:
		if msg.seq != m.fetchSeq {
			return m, nil
		}
		m.fetching = false
		m.lastErr = msg.err
		if msg.meta != nil {
			m.meta, m.views, m.viewIdx = msg.meta, msg.meta.Views, msg.viewIdx
			// reports name the project's main status field, whatever the view groups by
			m.hub.Get(m.owner, m.number).SetMeta(msg.meta.Title, resolveGroup(msg.meta.Fields, ""))
		}
		if msg.err == nil {
			if m.resetDiff || msg.group != m.groupField {
				m.board, m.history, m.moves, m.resetDiff = nil, nil, nil, false
			}
			m.groupField = msg.group
			m.applyBoard(msg.board)
			m.rememberBoard()
		}
		m.nextFetch = time.Now().Add(m.effectiveInterval())
		return m, m.loadMore() // the cursor may have scrolled further meanwhile

	case clearCopiedMsg:
		m.copied = ""
		return m, nil

	case linkMsg:
		if msg.added {
			if m.ruleChecking {
				m.recheck = true
				return m, nil
			}
			return m, m.checkRules()
		}
		return m, nil

	case rulesMsg:
		m.ruleChecking = false
		m.nextRules = time.Now().Add(m.effectiveInterval())
		for _, r := range msg.results {
			if r.err != nil {
				m.lastErr = r.err
				continue
			}
			if r.title != "" {
				r.w.SetMeta(r.title, r.group)
			}
			_, group := r.w.Meta()
			if group == "" {
				group = "Status"
			}
			r.w.Apply(r.matches, group, msg.at)
			if m.board != nil {
				m.board.RateLimit = r.rl
			}
		}
		if m.recheck {
			m.recheck = false
			return m, m.checkRules()
		}
		return m, nil

	case filterMsg:
		if msg.seq != m.filterSeq {
			return m, nil
		}
		return m, m.applySearch()

	case projectsMsg:
		m.gotProjects(msg)
		return m, nil

	case tea.KeyMsg:
		if m.confirm != confirmNone {
			return m.confirmKey(msg)
		}
		if m.settingsOpen {
			return m.settingsKey(msg)
		}
		if m.picker.open {
			return m.pickerKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *model) applyBoard(b *Board) {
	if m.groupField == "" {
		m.groupField = resolveGroup(b.Fields, "")
	}
	if !b.Windowed {
		tally(b, m.groupField)
	}
	evs := Diff(m.board, b, m.groupField)
	for _, e := range evs {
		if e.ItemID != "" {
			m.changedAt[e.ItemID] = e.At
		}
		if e.Kind == EvMoved {
			m.moves = append(m.moves, move{from: e.From, to: e.To, at: e.At})
		}
	}
	m.pruneMoves()
	first := m.board == nil
	m.board = b
	m.rebuild()
	if first { // open on the first column that has cards
		for i, c := range m.columns {
			if c.total > 0 {
				m.moveCol(i)
				break
			}
		}
	}
	counts := map[string]int{}
	for _, c := range m.columns {
		counts[c.name] = c.total
	}
	m.history = append(m.history, counts)
	if len(m.history) > historyLen {
		m.history = m.history[len(m.history)-historyLen:]
	}
}

// rebuild lays the snapshot's cards into columns in the board's column order.
func (m *model) rebuild() {
	if m.board == nil {
		return
	}
	colors := map[string]string{}
	for _, f := range m.board.Fields {
		if f.Name == m.groupField {
			for _, o := range f.Options {
				colors[o.Name] = o.Color
			}
		}
	}
	var cols []column
	idx := map[string]int{}
	for _, name := range m.board.Order {
		// "No <field>" leads, like on GitHub, but only when it has cards
		if name == "No "+m.groupField && m.board.Totals[name] == 0 {
			continue
		}
		idx[name] = len(cols)
		color := colors[name]
		if color == "" {
			color = "GRAY"
		}
		cols = append(cols, column{name: name, color: color, total: m.board.Totals[name]})
	}
	for _, it := range m.board.Items {
		if i, ok := idx[colName(it, m.groupField)]; ok {
			cols[i].items = append(cols[i].items, it)
		}
	}
	m.columns = cols
	m.clampCursor()
}

func (m *model) clampCursor() {
	if len(m.columns) == 0 {
		m.col, m.row = 0, 0
		return
	}
	m.col = clamp(m.col, 0, len(m.columns)-1)
	c := m.columns[m.col]
	m.row = clamp(m.row, 0, max(0, min(c.total, 100)-1)) // windowed columns load at most 100
	// scroll the column's 3-card window just enough to keep the cursor visible
	off := m.offsets[c.name]
	if m.row < off {
		off = m.row
	}
	if m.row >= off+visibleCards {
		off = m.row - visibleCards + 1
	}
	m.offsets[c.name] = clamp(off, 0, max(0, min(c.total, 100)-visibleCards))
	m.cursors[c.name] = m.row
}

// moveCol selects column i, restoring the cursor it had last time.
func (m *model) moveCol(i int) {
	if len(m.columns) == 0 {
		return
	}
	if c := clamp(i, 0, len(m.columns)-1); c != m.col {
		m.focusFrame = m.frame // an empty column's pet is startled awake
		m.col = c
	}
	m.row = m.cursors[m.columns[m.col].name]
	m.clampCursor()
}

func (m *model) resetCursors() {
	m.col, m.row = 0, 0
	m.cursors, m.offsets = map[string]int{}, map[string]int{}
}

func (m model) selected() (Item, bool) {
	if m.col < len(m.columns) && m.row < len(m.columns[m.col].items) {
		return m.columns[m.col].items[m.row], true
	}
	return Item{}, false
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch k.String() {
		case "enter":
			m.filtering = false
			m.filter.Blur()
			return m, m.applySearch()
		case "esc":
			m.filtering = false
			m.filter.Blur()
			m.filter.SetValue("")
			return m, m.applySearch()
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(k)
		m.filterSeq++
		seq := m.filterSeq
		return m, tea.Batch(cmd, tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return filterMsg{seq} }))
	}

	switch k.String() {
	case "ctrl+c": // deliberate: quits straight away
		return m, tea.Quit
	case "q", "Q":
		m.confirm = confirmQuitApp // Q is easy to hit by accident
		return m, nil
	case "tab", "shift+tab":
		if k.String() == "tab" {
			m.view = (m.view + 1) % numViews
		} else {
			m.view = (m.view + numViews - 1) % numViews
		}
		return m, m.statsNeedsAll()
	case "1":
		m.view = viewBoard
		return m, m.statsNeedsAll()
	case "2":
		m.view = viewStats
		return m, m.statsNeedsAll()
	case "3":
		m.view = viewLink
		return m, m.statsNeedsAll()
	case "left", "h":
		m.moveCol(m.col - 1)
	case "right", "l":
		m.moveCol(m.col + 1)
	case "up", "k":
		// scroll within the column; past the top, jump to the column above in the grid
		if fit := gridFit(m.width, len(m.columns)); m.row == 0 && m.col-fit >= 0 {
			m.moveCol(m.col - fit)
		} else {
			m.row--
			m.clampCursor()
		}
		return m, m.loadMore()
	case "down", "j":
		if fit := gridFit(m.width, len(m.columns)); m.col < len(m.columns) &&
			m.row >= min(m.columns[m.col].total, 100)-1 && m.col+fit < len(m.columns) {
			m.moveCol(m.col + fit)
		} else {
			m.row++
			m.clampCursor()
		}
		return m, m.loadMore()
	case "home", "g":
		m.row = 0
		m.clampCursor()
	case "end", "G":
		m.row = 1 << 30
		m.clampCursor()
		return m, m.loadMore()
	case " ", "space":
		// pausing asks first (Space is easy to hit by accident); resuming doesn't
		if !m.paused {
			if m.owner != "" {
				m.confirm = confirmPauseSync
			}
			return m, nil
		}
		m.paused = false
		if !m.fetching {
			return m, m.fetchCmd() // resume with a fresh sync
		}
		return m, nil
	case "p":
		return m, m.openPicker(pickProject)
	case "s", "S":
		m.settingsOpen, m.settingsCursor = true, 0
		return m, nil
	case "x", "X": // stop a watcher (AI link screen)
		if m.view == viewLink && m.hub.Status().Boards > 0 {
			return m, m.openPicker(pickWatcher)
		}
	case "c", "C":
		if m.view == viewLink {
			if m.hub.Status().Boards > 0 {
				if err := clipboard.WriteAll(samplePrompt()); err != nil {
					m.copied = "couldn't copy: " + err.Error()
				} else {
					m.copied = "sample prompt copied to the clipboard"
				}
				return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearCopiedMsg{} })
			}
		}
	case "/":
		m.filtering = true
		m.view = viewBoard
		return m, m.filter.Focus()
	case "esc":
		m.filter.SetValue("")
		return m, m.applySearch()
	case "v":
		if m.views == nil {
			return m, nil // views not loaded yet
		}
		return m, m.openPicker(pickView)
	case "r":
		if !m.fetching {
			return m, m.fetchCmd()
		}
	case "+", "=":
		m.interval += 5 * time.Second
		m.nextFetch = m.nextFetch.Add(5 * time.Second)
	case "-":
		if m.interval > 5*time.Second {
			m.interval -= 5 * time.Second
			m.nextFetch = m.nextFetch.Add(-5 * time.Second)
		}
	case "enter", "o":
		if it, ok := m.selected(); ok && it.URL != "" {
			openURL(it.URL)
		}
	case "O":
		if m.board != nil {
			openURL(m.board.URL)
		}
	}
	return m, nil
}

func openURL(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

// ---- styles ----

var ghColors = map[string]string{
	"GRAY": "#8b949e", "BLUE": "#58a6ff", "GREEN": "#3fb950", "YELLOW": "#d29922",
	"ORANGE": "#db6d28", "RED": "#f85149", "PINK": "#db61a2", "PURPLE": "#a371f7",
}

func ghColor(c string) lipgloss.Color {
	if h, ok := ghColors[c]; ok {
		return lipgloss.Color(h)
	}
	return lipgloss.Color(ghColors["GRAY"])
}

func stateColor(s string) lipgloss.Color {
	switch s {
	case "OPEN":
		return lipgloss.Color("#3fb950")
	case "CLOSED":
		return lipgloss.Color("#a371f7")
	case "MERGED":
		return lipgloss.Color("#a371f7")
	case "DRAFT":
		return lipgloss.Color("#8b949e")
	}
	return lipgloss.Color("#8b949e")
}

var (
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6e7681"))
	bold     = lipgloss.NewStyle().Bold(true)
	titleBar = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc")).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#388bfd")).Padding(0, 1)
	tabOn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#f0f6fc")).Underline(true)
	tabOff   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	selBar   = lipgloss.Color("#d1d7e0") // light grey cursor bar
	selText  = lipgloss.Color("#b1bac4")
	flash    = lipgloss.NewStyle().Foreground(lipgloss.Color("#e3b341")).Bold(true)
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149"))
)

// ---- view ----

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	header := m.headerView()
	footer := m.footerView()
	bodyH := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
	var body string
	switch {
	case m.board == nil && m.lastErr != nil:
		body = errStyle.Render("  " + m.lastErr.Error())
	case m.owner == "":
		body = ""
	case m.board == nil:
		body = "  " + m.spin.View() + " loading board…"
	case m.view == viewBoard:
		body = m.boardView(bodyH)
	case m.view == viewStats:
		body = m.statsView(bodyH)
	case m.view == viewLink:
		body = m.linkView(bodyH)
	}
	body = lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body)
	screen := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.confirm != confirmNone {
		return overlay(screen, m.confirmDialog(), m.width, m.height)
	}
	if m.settingsOpen {
		return overlay(screen, m.settingsDialog(), m.width, m.height)
	}
	if m.picker.open { // dialog floats over the dimmed current screen
		return overlay(screen, m.pickerBox(m.height-4), m.width, m.height)
	}
	return screen
}

func (m model) headerView() string {
	title := "CardHub"
	if m.owner != "" {
		title = fmt.Sprintf("%s/%d", m.owner, m.number)
	}
	if m.board != nil {
		title = m.board.Title
	}
	if v := m.currentView(); v != nil {
		title += " › " + v.Name
	}

	// line 1: project › view (full width), live status on the right
	var live string
	if m.board != nil {
		live = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("● LIVE")
		switch {
		case m.paused:
			live = lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Bold(true).Render("❚❚ PAUSED")
		case m.lastErr != nil:
			live = errStyle.Render("● STALE")
		}
	}
	// the version sits just left of the live status, dim
	if live != "" {
		live = dim.Render(versionLabel()) + "  " + live
	} else {
		live = dim.Render(versionLabel())
	}
	name := titleBar.Render(trunc(title, max(6, m.width-lipgloss.Width(live)-6)))
	line := lipgloss.JoinHorizontal(lipgloss.Center,
		name, strings.Repeat(" ", max(1, m.width-lipgloss.Width(name)-lipgloss.Width(live))), live)

	// line 2: screen tabs, grouping and item count on the right
	tabs := []string{"1 Board", "2 Stats", "3 AI link"}
	for i := range tabs {
		if view(i) == m.view && !m.picker.open {
			tabs[i] = tabOn.Render(tabs[i])
		} else {
			tabs[i] = tabOff.Render(tabs[i])
		}
	}
	menu := " " + strings.Join(tabs, "   ")
	var info string
	if m.board != nil {
		info = fmt.Sprintf("%s %s", bold.Render(fmt.Sprint(m.visibleCount())), dim.Render("items"))
		if lipgloss.Width(menu)+lipgloss.Width(info) >= m.width {
			info = ""
		}
	}
	line += "\n" + menu + strings.Repeat(" ", max(1, m.width-lipgloss.Width(menu)-lipgloss.Width(info))) + info

	if box := m.filterBox(); box != "" {
		line += "\n\n" + box // blank line between the menu and the filter box
	}
	return line + "\n"
}

func (m model) currentView() *ProjectView {
	if m.viewIdx >= 0 && m.viewIdx < len(m.views) {
		return &m.views[m.viewIdx]
	}
	return nil
}

func (m model) visibleCount() int {
	n := 0
	for _, c := range m.columns {
		n += c.total
	}
	return n
}

var (
	ftKey   = lipgloss.NewStyle().Foreground(lipgloss.Color("#e6edf3")).Bold(true)
	ftLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
	ftRule  = lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
)

// pair renders a "Key: Label" footer entry.
func pair(key, label string) string {
	return ftKey.Render(key) + dim.Render(":") + " " + ftLabel.Render(label)
}

// flow lays entries out left to right, filling each line up to width before
// wrapping to the next.
func flow(entries []string, width int, gap string) []string {
	var lines []string
	line := ""
	for _, e := range entries {
		switch {
		case line == "":
			line = e
		case lipgloss.Width(line)+lipgloss.Width(gap)+lipgloss.Width(e) <= width:
			line += gap + e
		default:
			lines = append(lines, line)
			line = e
		}
	}
	return append(lines, line)
}

// apiMeter shows how much of the hourly API budget is used, like a context-window
// meter: a bar that fills (green → yellow → red) as the budget is spent.
func apiMeter(rl RateLimit) (string, lipgloss.Color) {
	const w = 10
	if rl.Limit <= 0 {
		return ftLabel.Render("–"), lipgloss.Color("#8b949e")
	}
	used := clamp(rl.Limit-rl.Remaining, 0, rl.Limit)
	frac := float64(used) / float64(rl.Limit)
	fill := int(frac*w + 0.5)
	if used > 0 && fill == 0 {
		fill = 1
	}
	color := lipgloss.Color("#3fb950")
	switch {
	case rl.Remaining < lowRateLimit:
		color = lipgloss.Color("#f85149")
	case frac >= 0.5:
		color = lipgloss.Color("#d29922")
	}
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", fill)) +
		ftRule.Render(strings.Repeat("░", w-fill))
	pct := lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%d%%", int(frac*100+0.5)))
	// the bar and % already show the share; the count says how much is left
	detail := ftLabel.Render(fmt.Sprintf("%d left", rl.Remaining))
	if rl.Remaining < lowRateLimit {
		detail = errStyle.Render(fmt.Sprintf("%d left (throttled)", rl.Remaining))
	}
	reset := ""
	if left := time.Until(rl.ResetAt); left > 0 {
		reset = dim.Render(" · ") + ftLabel.Render("resets ") + ftKey.Render(untilLabel(left))
	}
	return bar + " " + pct + dim.Render(" · ") + detail + reset, color
}

// untilLabel formats a countdown compactly: 45s, 37m, 1h05m.
func untilLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()+0.5))
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// linkBody describes the AI watchers for the footer box: "ready · 0 rules ·
// ○ no agent waiting", later "2 boards · 3 rules · 10 sent · ● 1 agent waiting".
// The accent turns green while an agent is listening.
func (m model) linkBody() (string, lipgloss.Color, bool) {
	st := m.hub.Status()
	grey, green := lipgloss.Color("#8b949e"), lipgloss.Color("#3fb950")
	if st.Conflict != "" && m.owner != "" && st.Boards == 0 {
		return ftLabel.Render(st.Conflict), lipgloss.Color("#d29922"), true
	}
	if st.Boards == 0 {
		return "", grey, false
	}
	var parts []string
	if st.Boards > 1 {
		parts = append(parts, plural(st.Boards, "board"))
	}
	parts = append(parts, plural(st.Rules, "rule"), fmt.Sprintf("%d sent", st.Sent))
	if st.Boards == 1 && st.Rules == 0 && st.Sent == 0 { // nothing set up yet
		parts = []string{"ready", "0 rules"}
	}
	if st.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", st.Queued))
	}
	agent, accent := ftRule.Render("○")+" "+ftLabel.Render("no agent waiting"), grey
	if st.Waiting > 0 {
		label := "agent waiting"
		if st.Waiting > 1 {
			label = fmt.Sprintf("%d agents waiting", st.Waiting)
		}
		agent, accent = lipgloss.NewStyle().Foreground(green).Render("●")+" "+ftLabel.Render(label), green
	}
	return ftLabel.Render(strings.Join(parts, " · ")) + dim.Render(" · ") + agent, accent, true
}

// everyLabel shows the sync interval, marking it "auto" when it was stretched.
func everyLabel(base, eff time.Duration) string {
	if eff > base {
		return eff.String() + " (auto)"
	}
	return eff.String()
}

func (m model) footerView() string {
	var boxes []footBox

	// Sync: state, freshness, cadence (and the last error, if any)
	var sync []string
	switch {
	case m.fetching:
		sync = append(sync, m.spin.View()+" "+ftLabel.Render("Syncing…"))
	case m.paused:
		sync = append(sync, lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Bold(true).Render("❚❚ Paused")+
			dim.Render(" (Space resumes)"))
	case !m.nextFetch.IsZero():
		sync = append(sync, pair("Next", fmt.Sprintf("%ds", max(0, int(time.Until(m.nextFetch).Seconds())))))
	}
	if m.board != nil {
		sync = append(sync, pair("Updated", ago(m.board.FetchedAt)+" ago"),
			pair("Every", everyLabel(m.interval, m.effectiveInterval())))
	}
	if len(sync) > 0 {
		lines := []string{strings.Join(sync, "  ")}
		if m.lastErr != nil && m.board != nil {
			lines = append(lines, errStyle.Render(m.lastErr.Error()))
		}
		boxes = append(boxes, footBox{title: "Sync", accent: lipgloss.Color("#58a6ff"), lines: lines})
	}
	if m.board != nil {
		body, accent := apiMeter(m.board.RateLimit)
		boxes = append(boxes, footBox{title: "API budget", accent: accent, lines: []string{body}})
	}
	if body, accent, ok := m.linkBody(); ok {
		boxes = append(boxes, footBox{title: "AI link", accent: accent, lines: []string{body}})
	}

	// Tab cycles the screens; name the one it goes to
	other := map[view]string{viewBoard: "Stats", viewStats: "AI link", viewLink: "Board"}[m.view]
	keys := []string{
		pair("←↓↑→", "Move"), pair("⏎", "Open"), pair("P", "Project"), pair("V", "View"),
		pair("/", "Filter"), pair("R", "Sync"), pair("Space", pick(m.paused, "Resume", "Pause")), pair("S", "Settings"),
		pair("+/-", "Interval"), pair("Tab", other), pair("Q", "Quit"),
	}
	if m.view == viewLink && m.hub.Status().Boards > 0 {
		keys = append(keys[:len(keys)-1], pair("X", "Stop watcher"), pair("C", "Copy prompt"), keys[len(keys)-1])
	}
	shortcuts := footBox{title: "Shortcuts", accent: lipgloss.Color("#8b949e"), lines: flow(keys, m.width-4, "   ")}

	return "\n" + boxRows(boxes, m.width) + "\n" + boxRows([]footBox{shortcuts}, m.width)
}

func (m model) cardView(it Item, w int, sel bool) string {
	// the selected card swaps its state-coloured bar for a light-grey one and brightens its text
	bar := lipgloss.NewStyle().Foreground(stateColor(it.State)).Render("▌")
	titleSt, metaSt := bold, dim
	if sel {
		bar = lipgloss.NewStyle().Foreground(selBar).Bold(true).Render("┃")
		titleSt = bold.Foreground(lipgloss.Color("#ffffff"))
		metaSt = lipgloss.NewStyle().Foreground(selText)
	}
	mark := " "
	if t, ok := m.changedAt[it.ID]; ok && time.Since(t) < flashFor {
		mark = flash.Render("●")
	}
	tw := w - 3
	l1 := trunc(it.Title, tw)
	meta := it.Ref()
	if it.Repo != "" {
		// drop owner prefix to save room
		if _, r, ok := strings.Cut(meta, "/"); ok {
			meta = r
		}
	}
	l2 := metaSt.Render(trunc(meta+" · "+strings.ToLower(it.State), tw))
	who := ""
	if len(it.Assignees) > 0 {
		who = "@" + strings.Join(it.Assignees, " @")
	}
	if len(it.Labels) > 0 {
		if who != "" {
			who += "  "
		}
		who += "[" + strings.Join(it.Labels, "] [") + "]"
	}
	l3 := metaSt.Render(trunc(who, tw))

	lines := []string{bar + mark + titleSt.Render(l1), bar + " " + l2, bar + " " + l3}
	return strings.Join(lines, "\n") + "\n"
}

func (m model) statsView(h int) string {
	total := m.visibleCount()
	labelW := 4
	for _, c := range m.columns {
		labelW = max(labelW, runewidth.StringWidth(c.name))
	}
	labelW = min(labelW, 28)
	barW := clamp(m.width-labelW-40-historyLen, 10, 60)

	var b strings.Builder
	b.WriteString(bold.Render("Items by "+m.groupField) + dim.Render(fmt.Sprintf("  (%d total)", total)) + "\n\n")
	maxN := 1
	for _, c := range m.columns {
		maxN = max(maxN, c.total)
	}
	for _, c := range m.columns {
		n := c.total
		fill := n * barW / maxN
		if n > 0 && fill == 0 {
			fill = 1
		}
		bar := lipgloss.NewStyle().Foreground(ghColor(c.color)).Render(strings.Repeat("█", fill)) +
			dim.Render(strings.Repeat("░", barW-fill))
		pct := 0.0
		if total > 0 {
			pct = 100 * float64(n) / float64(total)
		}
		name := runewidth.FillRight(trunc(c.name, labelW), labelW)
		b.WriteString(fmt.Sprintf(" %s %s %4d %s  %s\n", name, bar, n, dim.Render(fmt.Sprintf("%3.0f%%", pct)), m.sparkline(c.name)))
	}
	b.WriteString("\n" + strings.Repeat(" ", labelW+barW+15) + dim.Render(fmt.Sprintf("trend over last %d syncs", len(m.history))) + "\n\n")

	// state + assignee breakdowns side by side
	state := map[string]int{}
	people := map[string]int{}
	for _, c := range m.columns {
		for _, it := range c.items {
			state[it.State]++
			if len(it.Assignees) == 0 {
				people["(unassigned)"]++
			}
			for _, a := range it.Assignees {
				people[a]++
			}
		}
	}
	left := bold.Render("By state") + "\n" + table(state, 6, func(k string) lipgloss.Color { return stateColor(k) })
	right := bold.Render("Top assignees") + "\n" + table(people, max(3, h-len(m.columns)-12), nil)
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(32).PaddingLeft(1).Render(left),
		lipgloss.NewStyle().PaddingLeft(4).Render(right)))
	return b.String()
}

func table(counts map[string]int, limit int, color func(string) lipgloss.Color) string {
	type kv struct {
		k string
		v int
	}
	var rows []kv
	for k, v := range counts {
		rows = append(rows, kv{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].v != rows[j].v {
			return rows[i].v > rows[j].v
		}
		return rows[i].k < rows[j].k
	})
	var b strings.Builder
	for i, r := range rows {
		if i >= limit {
			b.WriteString(dim.Render(fmt.Sprintf("  … %d more\n", len(rows)-limit)))
			break
		}
		name := runewidth.FillRight(trunc(strings.ToLower(r.k), 18), 18)
		if color != nil {
			name = lipgloss.NewStyle().Foreground(color(r.k)).Render(name)
		}
		b.WriteString(fmt.Sprintf("  %s %4d\n", name, r.v))
	}
	return b.String()
}

var sparks = []rune("▁▂▃▄▅▆▇█")

func (m model) sparkline(col string) string {
	if len(m.history) < 2 {
		return ""
	}
	lo, hi := 1<<30, 0
	for _, h := range m.history {
		lo, hi = min(lo, h[col]), max(hi, h[col])
	}
	var b strings.Builder
	for _, h := range m.history {
		i := 0
		if hi > lo {
			i = (h[col] - lo) * (len(sparks) - 1) / (hi - lo)
		}
		b.WriteRune(sparks[i])
	}
	first, last := m.history[0][col], m.history[len(m.history)-1][col]
	delta := ""
	switch {
	case last > first:
		delta = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render(fmt.Sprintf(" +%d", last-first))
	case last < first:
		delta = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149")).Render(fmt.Sprintf(" %d", last-first))
	}
	return dim.Render(b.String()) + delta
}

// ---- helpers ----

func trunc(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if w <= 1 {
		return ""
	}
	return runewidth.Truncate(s, w, "…")
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return d.Round(time.Minute).String()
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
