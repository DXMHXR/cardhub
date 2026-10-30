package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Picker: an overlay menu for switching project ("p") or saved view ("v").
// Typing narrows the list; enter applies the choice to the live board.

type pickKind int

const (
	pickProject pickKind = iota
	pickView
	pickWatcher // stop one of the AI watchers
)

type projectsMsg struct {
	list []ProjectRef
	err  error
}

type picker struct {
	open    bool
	kind    pickKind
	query   string
	cursor  int
	loading bool // project list in flight
	split   bool // pickProject: open the project as a second board (\)
	err     error
	list    []ProjectRef // cached project list
}

// pickRow is one menu entry, whichever kind of menu is open.
type pickRow struct {
	meta, title string
	current     bool
	proj        ProjectRef // pickProject
	view        int        // pickView: index into m.views, -1 = all items
	board       string     // pickWatcher: owner/number
}

func (m model) pickRows() []pickRow {
	var rows []pickRow
	switch m.picker.kind {
	case pickProject:
		for _, p := range m.picker.list {
			rows = append(rows, pickRow{
				meta: p.Owner + fmt.Sprintf(" #%d", p.Number), title: p.Title,
				current: p.Owner == m.owner && p.Number == m.number, proj: p,
			})
		}
	case pickWatcher:
		for _, s := range m.hub.Snapshots() {
			if !s.Active {
				continue
			}
			detail := plural(len(s.Rules), "rule")
			if s.Waiting {
				detail += " · agent waiting"
			}
			if s.OnScreen {
				detail += " · on screen"
			}
			rows = append(rows, pickRow{meta: s.Board, title: firstNonEmpty(s.Title, s.Board) + "  ·  " + detail,
				current: s.Waiting, board: s.Board})
		}
	case pickView:
		rows = append(rows, pickRow{meta: "", title: "All items", current: m.viewIdx < 0, view: -1})
		for i, v := range m.views {
			rows = append(rows, pickRow{
				meta: fmt.Sprintf("#%d", v.Number), title: v.Name,
				current: i == m.viewIdx, view: i,
			})
		}
	}
	return rows
}

// matches filters rows by the typed query; title hits rank above meta-only hits.
func (m model) pickMatches() []pickRow {
	rows := m.pickRows()
	q := strings.ToLower(strings.TrimSpace(m.picker.query))
	if q == "" {
		return rows
	}
	var strong, weak []pickRow
	for _, r := range rows {
		hay := strings.ToLower(r.title + " " + r.meta)
		ok := true
		for _, w := range strings.Fields(q) {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		switch {
		case ok && strings.Contains(strings.ToLower(r.title), q):
			strong = append(strong, r)
		case ok:
			weak = append(weak, r)
		}
	}
	return append(strong, weak...)
}

func (m *model) focusCurrent() {
	m.picker.cursor = 0
	for i, r := range m.pickRows() {
		if r.current {
			m.picker.cursor = i
		}
	}
}

func (m *model) openPicker(kind pickKind) tea.Cmd {
	m.picker.open, m.picker.kind, m.picker.query, m.picker.split = true, kind, "", false
	m.focusCurrent()
	if kind != pickProject || m.picker.list != nil || m.picker.loading {
		return nil
	}
	m.picker.loading, m.picker.err = true, nil
	return listProjects(m.client)
}

func listProjects(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := c.ListProjects(ctx)
		return projectsMsg{list, err}
	}
}

func (m *model) gotProjects(msg projectsMsg) {
	m.picker.loading, m.picker.err = false, msg.err
	if msg.err == nil {
		m.picker.list = msg.list
		if m.picker.list == nil {
			m.picker.list = []ProjectRef{}
		}
		if m.picker.kind == pickProject {
			m.focusCurrent()
		}
	}
}

func (m model) pickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	list := m.pickMatches()
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.picker.query != "" {
			m.picker.query, m.picker.cursor = "", 0
		} else if m.owner != "" { // with no board yet there's nothing to go back to
			m.picker.open = false
		}
	case "up", "ctrl+p":
		m.picker.cursor = max(0, m.picker.cursor-1)
	case "down", "ctrl+n":
		m.picker.cursor = min(max(0, len(list)-1), m.picker.cursor+1)
	case "backspace":
		if r := []rune(m.picker.query); len(r) > 0 {
			m.picker.query, m.picker.cursor = string(r[:len(r)-1]), 0
		}
	case "enter":
		if m.picker.cursor < len(list) {
			m.picker.open = false
			row := list[m.picker.cursor]
			switch m.picker.kind {
			case pickProject:
				if m.picker.split {
					return m, m.openSplit(row.proj)
				}
				return m, m.switchProject(row.proj)
			case pickWatcher:
				m.confirm, m.stopTarget = confirmStopWatcher, row.board
				return m, nil
			}
			return m, m.switchView(row.view)
		}
	default:
		if k.Type == tea.KeyRunes || k.Type == tea.KeySpace {
			m.picker.query += string(k.Runes)
			m.picker.cursor = 0
		}
	}
	return m, nil
}

// switchProject points the live board at another project, starting fresh.
func (m *model) switchProject(p ProjectRef) tea.Cmd {
	if p.Owner == m.owner && p.Number == m.number {
		return nil
	}
	m.owner, m.number = p.Owner, p.Number
	m.showOnHub() // a watcher for the new project; the others keep running
	m.board, m.meta, m.views, m.viewIdx, m.viewArg = nil, nil, nil, -1, ""
	m.groupField, m.columns, m.moves = "", nil, nil
	m.changedAt = map[string]time.Time{}
	m.resetDiff, m.lastErr = false, nil
	m.filter.SetValue("")
	m.query = ""
	m.restore = m.saved.Searches // its first sync brings back the search saved for its view
	m.resetCursors()
	m.view = viewBoard
	return m.fetchCmd() // supersedes any in-flight sync of the old project
}

// switchView applies saved view i (-1 = all items) and refetches with its
// filter. Each view keeps its own "/" search: the one left is saved, the new
// one's comes back (none if it never had one).
func (m *model) switchView(i int) tea.Cmd {
	if i == m.viewIdx {
		return nil
	}
	m.rememberBoard()
	m.viewIdx = i
	name := ""
	if v := m.currentView(); v != nil {
		name = v.Name
	}
	m.filter.SetValue(m.saved.Searches[searchKey(m.owner, m.number, name)])
	if i >= 0 && m.views[i].GroupBy != "" {
		m.groupField = m.views[i].GroupBy
	}
	m.resetDiff = true
	m.resetCursors()
	m.view = viewBoard
	return m.fetchCmd() // supersedes any in-flight sync
}

// pickerBox renders the dialog itself, at most maxW wide and maxH lines tall.
func (m model) pickerBox(maxW, maxH int) string {
	h := maxH
	w := clamp(maxW-8, 30, 64)
	inner := w - 4
	accent := lipgloss.Color("#388bfd")
	title, what := "Switch project", "projects"
	switch {
	case m.owner == "":
		title = "Pick a project to watch"
	case m.picker.split:
		title = "Open a second board"
	}
	switch m.picker.kind {
	case pickView:
		title, what = "Switch view", "views"
	case pickWatcher:
		title, what = "Stop an AI watcher", "watchers"
	}

	var b strings.Builder
	if m.split && !m.picker.split && m.picker.kind != pickWatcher { // name the board it switches
		title = bold.Render(title) + dim.Render(" · "+trunc(m.paneName(), inner-lipgloss.Width(title)-3))
	} else {
		title = bold.Render(title)
	}
	b.WriteString(title + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(accent).Render("› ") + m.picker.query +
		lipgloss.NewStyle().Foreground(accent).Render("▏") + "\n\n")

	list := m.pickMatches()
	switch {
	case m.picker.loading:
		b.WriteString(m.spin.View() + dim.Render(" loading your "+what+"…"))
	case m.picker.err != nil:
		b.WriteString(errStyle.Render(trunc(m.picker.err.Error(), inner)))
	case len(list) == 0 && m.picker.query == "" && m.picker.kind == pickProject:
		b.WriteString(dim.Render("You have no open projects. Open one by URL instead:\n  cardhub https://github.com/orgs/<org>/projects/<number>\nor run `cardhub doctor` to check your token's access."))
	case len(list) == 0:
		b.WriteString(dim.Render("no matching " + what))
	default:
		rows := max(1, h-8)
		start := clamp(m.picker.cursor-rows+1, 0, max(0, len(list)-rows))
		metaW := 0
		for _, r := range list {
			metaW = max(metaW, len([]rune(r.meta)))
		}
		metaW = min(metaW, 20)
		for i := start; i < len(list) && i < start+rows; i++ {
			r := list[i]
			bar, titleSt, metaSt := " ", lipgloss.NewStyle().Foreground(lipgloss.Color("#c9d1d9")), dim
			if i == m.picker.cursor {
				bar = lipgloss.NewStyle().Foreground(selBar).Bold(true).Render("┃")
				titleSt, metaSt = bold.Foreground(lipgloss.Color("#ffffff")), lipgloss.NewStyle().Foreground(selText)
			}
			mark := "  "
			if r.current {
				mark = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Render("● ")
			}
			meta := ""
			if metaW > 0 {
				meta = fmt.Sprintf("%-*s  ", metaW, trunc(r.meta, metaW))
			}
			t := trunc(r.title, inner-lipgloss.Width(meta)-4)
			b.WriteString(bar + " " + mark + metaSt.Render(meta) + titleSt.Render(t) + "\n")
		}
		if len(list) > rows {
			b.WriteString(dim.Render(fmt.Sprintf("  %d–%d of %d", start+1, min(start+rows, len(list)), len(list))))
		}
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1).Width(w - 2).
		Render(strings.TrimRight(b.String(), "\n") + "\n\n" + dim.Render(m.pickerHelp()))
	return box
}

func (m model) pickerHelp() string {
	if m.owner == "" {
		return "type to search · ↑↓ choose · enter open · ctrl+c quit"
	}
	if m.picker.kind == pickWatcher {
		return "type to search · ↑↓ choose · enter stop · esc close"
	}
	return "type to search · ↑↓ choose · enter open · esc close"
}

var backdrop = lipgloss.NewStyle().Foreground(lipgloss.Color("#3d444d"))

// overlay draws dialog centred (both ways) over screen, which is dimmed to a flat grey
// backdrop so the dialog stands out while the board stays visible behind it.
func overlay(screen, dialog string, width, height int) string {
	return overlayIn(screen, dialog, width, height, rect{0, 0, width, height})
}

// rect is an area of the screen: its top-left corner and size.
type rect struct{ x, y, w, h int }

// overlayIn is overlay with the dialog centred on area r instead of the whole
// screen (kept on screen when it's bigger than r).
func overlayIn(screen, dialog string, width, height int, r rect) string {
	bg := strings.Split(screen, "\n")
	for len(bg) < height {
		bg = append(bg, "")
	}
	for i, l := range bg {
		bg[i] = backdrop.Render(ansi.Strip(l))
	}
	fg := strings.Split(dialog, "\n")
	dw := lipgloss.Width(dialog)
	x := clamp(r.x+(r.w-dw)/2, 0, max(0, width-dw))
	y := clamp(r.y+(r.h-len(fg))/2, 0, max(0, height-len(fg)))
	for i, l := range fg {
		row := y + i
		if row >= len(bg) {
			break
		}
		line := bg[row]
		left := ansi.Truncate(line, x, "")
		if pad := x - ansi.StringWidth(left); pad > 0 {
			left += strings.Repeat(" ", pad)
		}
		right := ansi.TruncateLeft(line, x+dw, "")
		bg[row] = left + l + right
	}
	return strings.Join(bg[:height], "\n")
}
