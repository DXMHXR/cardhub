package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Split screen (\): two boards at once, from one project (in different views)
// or from two. They sit side by side when each still gets two columns abreast,
// else one above the other. Keys work on the focused board; W moves the focus.
// Each board syncs on its own; the stats and AI link screens show the focused
// one.

const splitGap = 3 // " │ " between boards side by side

// splitAcross reports whether a split's boards sit side by side: when each is
// still wide enough for two columns abreast.
func (m model) splitAcross() bool {
	return (m.width-splitGap)/2 >= 2*minBoxW+gapW
}

// boardSize is the area a board gets between header and footer: all of it, or
// its half of a split less the board's title line.
func (m model) boardSize() (w, h int) {
	w, h = m.width, m.boardH()
	switch {
	case !m.split || m.view != viewBoard:
		return w, h
	case m.splitAcross():
		return (w - splitGap) / 2, h - 1
	}
	return w, (h-1)/2 - 1 // a blank line between the boards
}

func (m model) boardW() int {
	w, _ := m.boardSize()
	return w
}

// splitView draws both boards of a split, each in its slot.
func (m model) splitView() string {
	w, h := m.boardSize()
	o := m
	o.pane, o.other, o.bg = m.other, m.pane, true
	first, second := m.paneView(w, h), o.paneView(w, h)
	if m.slot == 1 {
		first, second = second, first
	}
	if !m.splitAcross() {
		return first + "\n\n" + second
	}
	sep := make([]string, h+1)
	for i := range sep {
		sep[i] = ftRule.Render(" │ ")
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, first, strings.Join(sep, "\n"), second)
}

// paneView is one board of a split, w wide: its title line, then h lines of board.
func (m model) paneView(w, h int) string {
	var body string
	switch {
	case m.board == nil && m.lastErr != nil:
		body = errStyle.Render("  " + trunc(m.lastErr.Error(), w-2))
	case m.board == nil:
		body = "  " + m.spin.View() + " loading board…"
	default:
		body = m.boardView(w, h)
	}
	body = lipgloss.NewStyle().Width(w).MaxWidth(w).Height(h).MaxHeight(h).Render(body)
	return m.paneTitle(w) + "\n" + body
}

// focusedRect is where the focused board of a split sits on screen, title
// line included; false when no split is on screen.
func (m model) focusedRect() (rect, bool) {
	if !m.split || m.view != viewBoard {
		return rect{}, false
	}
	w, h := m.boardSize()
	top := lipgloss.Height(m.headerView())
	if m.splitAcross() {
		return rect{m.slot * (w + splitGap), top, w, h + 1}, true
	}
	return rect{0, top + m.slot*(h+2), w, h + 1}, true
}

// paneName is the board's project and view, as its title line shows them.
func (m model) paneName() string {
	title := fmt.Sprintf("%s/%d", m.owner, m.number)
	if m.board != nil {
		title = m.board.Title
	}
	if v := m.currentView(); v != nil {
		title += " › " + v.Name
	}
	return title
}

// paneTitle names a split's board: bright with a blue dot when it has the
// focus, dim otherwise, with its item count (or sync state) on the right.
func (m model) paneTitle(w int) string {
	title := m.paneName()
	var info string
	switch {
	case m.lastErr != nil && m.board != nil:
		info = errStyle.Render("● STALE")
	case m.fetching:
		info = m.spin.View()
	}
	if m.board != nil {
		info = strings.TrimSpace(info + " " + ftLabel.Render(fmt.Sprintf("%d items", m.visibleCount())))
	}
	dot, titleSt := ftRule.Render("○ "), ftLabel
	if !m.bg {
		dot, titleSt = lipgloss.NewStyle().Foreground(lipgloss.Color("#388bfd")).Bold(true).Render("● "), bold.Foreground(lipgloss.Color("#f0f6fc"))
	}
	name := dot + titleSt.Render(trunc(title, max(4, w-lipgloss.Width(info)-4)))
	return name + strings.Repeat(" ", max(1, w-lipgloss.Width(name)-lipgloss.Width(info))) + info
}

// toggleSplit opens a second board (picked from the project list), or closes
// the split, keeping the focused board.
func (m *model) toggleSplit() tea.Cmd {
	if m.split {
		m.split, m.other = false, pane{}
		m.clampCursor()
		return m.loadMore()
	}
	cmd := m.openPicker(pickProject)
	m.picker.split = true
	return cmd
}

// openSplit shows project p next to the board on screen and focuses it. The
// same project again starts on the view menu, to show it in another view.
func (m *model) openSplit(p ProjectRef) tea.Cmd {
	np := newPane(p.Owner, p.Number, "", "")
	np.slot = 1 - m.slot
	same := p.Owner == m.owner && p.Number == m.number
	if same { // the project's views are known: no need to load them again
		np.meta, np.views = m.meta, m.views
	}
	m.other, m.pane, m.split = m.pane, np, true
	m.view = viewBoard
	if m.hub != nil {
		m.hub.Show(p.Owner, p.Number)
	}
	m.onOther(func() tea.Cmd { m.clampCursor(); return nil }) // the board that moved over got smaller
	cmd := m.fetchCmd()
	if same && m.views != nil {
		m.openPicker(pickView)
	}
	return cmd
}

// crossBoards moves the cursor onto the split's other board when key (an
// arrow or hjkl) runs off this board's edge toward it, landing on the facing
// edge: the same grid row when side by side, the same place in the row when
// stacked. ok is false when the key stays on this board.
func (m *model) crossBoards(key string) (cmd tea.Cmd, ok bool) {
	n := len(m.columns)
	if !m.split || m.view != viewBoard || n == 0 {
		return nil, false
	}
	fit := gridFit(m.boardW(), n)
	r, c := m.col/fit, m.col%fit
	lastRow := m.col+fit >= n
	atTop := m.row == 0 || m.shownCards() == 0
	atEnd := m.row >= min(m.columns[m.col].total, 100)-1 || m.shownCards() == 0
	across := m.splitAcross()
	switch key {
	case "left", "h":
		ok = across && m.slot == 1 && c == 0
	case "right", "l":
		ok = across && m.slot == 0 && (c == fit-1 || m.col == n-1)
	case "up", "k":
		ok = !across && m.slot == 1 && r == 0 && atTop
	case "down", "j":
		ok = !across && m.slot == 0 && lastRow && atEnd
	}
	if !ok {
		return nil, false
	}
	cmd = m.switchBoard()
	n2 := len(m.columns)
	if n2 == 0 {
		return cmd, true
	}
	fit2 := gridFit(m.boardW(), n2)
	rows2 := (n2 + fit2 - 1) / fit2
	var to int
	switch key {
	case "left", "h": // the right end of the same row
		to = min(min(r, rows2-1)*fit2+fit2-1, n2-1)
	case "right", "l": // the left end of the same row
		to = min(r, rows2-1) * fit2
	case "up", "k": // the bottom row
		to = min((rows2-1)*fit2+c, n2-1)
	case "down", "j": // the top row
		to = min(c, n2-1)
	}
	m.moveCol(to)
	return tea.Batch(cmd, m.loadMore()), true
}

// swapBoards trades the split's boards' places: the second becomes the first
// (left or top). The focus stays on the board it was on.
func (m *model) swapBoards() {
	if m.split {
		m.slot, m.other.slot = m.other.slot, m.slot
	}
}

// switchBoard moves the focus to the split's other board.
func (m *model) switchBoard() tea.Cmd {
	if !m.split {
		return nil
	}
	m.pane, m.other = m.other, m.pane
	m.focusFrame = m.frame // its pet is startled awake
	if m.hub != nil {
		m.hub.Show(m.owner, m.number)
	}
	m.rememberBoard()
	m.clampCursor()
	return m.loadMore()
}
