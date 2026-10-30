package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Table views (TABLE_LAYOUT) show as a table: one box like a board's column,
// a row per item in the project's order and a column per field the view
// shows. Each row's bar takes its Status column's colour, as cards do. When
// the columns don't fit, the ones on the right go first; Title, Status and
// iteration fields (like a release plan) always stay, and Title gives up
// width before they do.

const (
	tableGap      = 2  // between table columns
	tableChrome   = 6  // box border, head, column names, rule and scroll line
	tableTitleMin = 30 // Title width other columns make room for
	tableCellMax  = 24 // widest a column other than Title gets
)

// isTable reports whether the board on screen is a table view's snapshot.
func (m model) isTable() bool { return m.board != nil && m.board.Table }

// boardOrTable draws the snapshot the way its view lays it out.
func (m model) boardOrTable(w, h int) string {
	if m.isTable() {
		return m.tableView(w, h)
	}
	return m.boardView(w, h)
}

// tableRows is how many rows the table on screen has room for.
func (m model) tableRows() int {
	_, h := m.boardSize()
	return max(1, h-tableChrome)
}

// tableLimit is how many rows the next table sync loads: what's on screen
// plus a screenful of read-ahead (enough for the first screen before the
// terminal's size is known).
func (m model) tableLimit() int {
	return min(100, max(30, m.tableOff+2*m.tableRows()))
}

// tableReach is how many rows the cursor can get to: windowed syncs load at most 100.
func (m model) tableReach() int {
	if !m.isTable() {
		return 0
	}
	return min(m.board.Rows, 100)
}

// clampTable keeps the table's cursor on a row and scrolls just enough to show it.
func (m *model) clampTable() {
	if !m.isTable() {
		return
	}
	reach, vis := m.tableReach(), m.tableRows()
	m.tableRow = clamp(m.tableRow, 0, max(0, reach-1))
	if m.tableRow < m.tableOff {
		m.tableOff = m.tableRow
	}
	if m.tableRow >= m.tableOff+vis {
		m.tableOff = m.tableRow - vis + 1
	}
	m.tableOff = clamp(m.tableOff, 0, max(0, reach-vis))
}

// tableKey moves the table's cursor; ok is false for keys tables don't take over.
func (m *model) tableKey(key string) (cmd tea.Cmd, ok bool) {
	switch key {
	case "up", "k":
		m.tableRow--
	case "down", "j":
		m.tableRow++
	case "pgup":
		m.tableRow -= m.tableRows()
	case "pgdown":
		m.tableRow += m.tableRows()
	case "home", "g":
		m.tableRow = 0
	case "end", "G":
		m.tableRow = 1 << 30
	case "left", "h", "right", "l": // one column of rows: nowhere to go
		return nil, true
	default:
		return nil, false
	}
	m.clampTable()
	return m.loadMore(), true
}

// crossFromTable is crossBoards for a table: it leaves by its side facing the
// other board, or by its first or last row when the boards are stacked.
func (m *model) crossFromTable(key string) (cmd tea.Cmd, ok bool) {
	across := m.splitAcross()
	switch key {
	case "left", "h":
		ok = across && m.slot == 1
	case "right", "l":
		ok = across && m.slot == 0
	case "up", "k":
		ok = !across && m.slot == 1 && m.tableRow == 0
	case "down", "j":
		ok = !across && m.slot == 0 && m.tableRow >= m.tableReach()-1
	}
	if !ok {
		return nil, false
	}
	cmd = m.switchBoard()
	if m.isTable() {
		return tea.Batch(cmd, m.enterTable(key)), true
	}
	n := len(m.columns)
	if n == 0 {
		return cmd, true
	}
	fit := gridFit(m.boardW(), n)
	switch key {
	case "left", "h": // the right end of the top row
		m.moveCol(min(fit, n) - 1)
	case "right", "l", "down", "j": // the first column
		m.moveCol(0)
	case "up", "k": // the bottom row
		m.moveCol((n - 1) / fit * fit)
	}
	return tea.Batch(cmd, m.loadMore()), true
}

// enterTable puts the cursor on the row facing the board key came from.
func (m *model) enterTable(key string) tea.Cmd {
	switch key {
	case "up", "k":
		m.tableRow = m.tableReach() - 1
	case "down", "j":
		m.tableRow = 0
	}
	m.clampTable()
	return m.loadMore()
}

// tableCol is one column of the table on screen.
type tableCol struct {
	f    ViewField
	w    int
	keep bool // stays however narrow the screen
}

// tableColumns picks the columns that fit inner width and their widths;
// Title comes first and takes what the others leave. hidden counts the
// columns left out.
func (m model) tableColumns(inner int) (title int, cols []tableCol, hidden int) {
	fields := m.board.TableFields
	if len(fields) == 0 { // the view's fields unknown: the essentials
		fields = []ViewField{{"Title", "TITLE"}, {m.groupField, "SINGLE_SELECT"}}
	}
	for _, f := range fields {
		if f.Type == "TITLE" {
			continue
		}
		w := min(runewidth.StringWidth(f.Name), 12) // long names get truncated
		for _, it := range m.board.Items {
			w = max(w, runewidth.StringWidth(m.cellText(it, f)))
		}
		keep := f.Name == m.groupField || strings.EqualFold(f.Name, "Status") || f.Type == "ITERATION"
		cols = append(cols, tableCol{f, min(w, tableCellMax), keep})
	}
	left := func() int {
		n := inner - 2 // bar and change mark
		for _, c := range cols {
			n -= tableGap + c.w
		}
		return n
	}
	// make room for Title: drop columns from the right, the ones that stay last
	for left() < min(tableTitleMin, inner/2) {
		i := len(cols) - 1
		for i >= 0 && cols[i].keep {
			i--
		}
		if i < 0 {
			break
		}
		cols = append(cols[:i], cols[i+1:]...)
		hidden++
	}
	// still too narrow: the columns that stay shrink too, widest first
	for left() < 12 {
		widest := -1
		for i, c := range cols {
			if c.w > 6 && (widest < 0 || c.w > cols[widest].w) {
				widest = i
			}
		}
		if widest < 0 {
			break
		}
		cols[widest].w--
	}
	return max(left(), 4), cols, hidden
}

// cellText is an item's value for a table column, as plain text.
func (m model) cellText(it Item, f ViewField) string {
	switch f.Type {
	case "TITLE":
		return it.Title
	case "ASSIGNEES":
		if len(it.Assignees) == 0 {
			return ""
		}
		return "@" + strings.Join(it.Assignees, " @")
	case "LABELS":
		return strings.Join(it.Labels, ", ")
	case "REPOSITORY":
		_, r, _ := strings.Cut(it.Repo, "/")
		return r
	case "MILESTONE":
		return it.Milestone
	case "PARENT_ISSUE":
		return it.Parent
	case "LINKED_PULL_REQUESTS":
		var prs []string
		for _, n := range it.LinkedPRs {
			prs = append(prs, fmt.Sprintf("#%d", n))
		}
		return strings.Join(prs, " ")
	case "SUB_ISSUES_PROGRESS":
		if it.SubTotal == 0 {
			return ""
		}
		done := it.SubDone * 5 / it.SubTotal
		return strings.Repeat("▰", done) + strings.Repeat("▱", 5-done) + fmt.Sprintf(" %d/%d", it.SubDone, it.SubTotal)
	case "UPDATED":
		return ago(it.UpdatedAt) + " ago"
	}
	if f.Name == m.groupField && f.Type == "SINGLE_SELECT" {
		return "● " + colName(it, m.groupField)
	}
	return it.Values[f.Name]
}

// rowColor is the colour of the board column an item sits in (its Status).
func (m model) rowColor(it Item) lipgloss.Color {
	name := colName(it, m.groupField)
	for _, c := range m.columns {
		if c.name == name {
			return ghColor(c.color)
		}
	}
	return ghColor("GRAY")
}

// fitCell truncates s to w and pads it to exactly w.
func fitCell(s string, w int) string {
	s = trunc(s, w)
	return s + strings.Repeat(" ", max(0, w-runewidth.StringWidth(s)))
}

// tableView draws a table view's rows in a w×h area: a box with the view's
// name and row count, the column names, then a row per item.
func (m model) tableView(w, h int) string {
	b := m.board
	inner := w - 4 // border + padding
	sel, hasSel := m.selected()
	accent := lipgloss.Color("#388bfd")
	if hasSel {
		accent = m.rowColor(sel)
	}
	focused := !m.bg

	name := "Table"
	if v := m.currentView(); v != nil {
		name = v.Name
	}
	titleW, cols, hidden := m.tableColumns(inner)
	count := dim.Render(fmt.Sprint(b.Rows))
	if hidden > 0 {
		count = dim.Render(fmt.Sprintf("%s hidden · ", plural(hidden, "column"))) + count
	}
	nameSt := bold
	if focused {
		nameSt = bold.Foreground(accent)
	}
	headName := lipgloss.NewStyle().Foreground(accent).Render("▦") + " " +
		nameSt.Render(trunc(name, inner-lipgloss.Width(count)-3))
	head := headName + strings.Repeat(" ", max(1, inner-lipgloss.Width(headName)-lipgloss.Width(count))) + count

	names := "  " + fitCell("Title", titleW)
	for _, c := range cols {
		names += strings.Repeat(" ", tableGap) + fitCell(c.f.Name, c.w)
	}
	ruleCh := pick(focused, "━", "─")
	lines := []string{head, ftLabel.Bold(true).Render(names),
		lipgloss.NewStyle().Foreground(accent).Faint(!focused).Render(strings.Repeat(ruleCh, inner))}

	reach := m.tableReach()
	vis := max(1, h-tableChrome)
	off := clamp(m.tableOff, 0, max(0, reach-vis))
	shown := clamp(reach-off, 1, vis)
	switch {
	case reach == 0:
		lines = append(lines, dim.Render("  no items match"))
	default:
		for i := off; i < off+shown; i++ {
			if i >= len(b.Items) { // scrolled onto rows the next sync is fetching
				lines = append(lines, " "+m.spin.View()+dim.Render(" loading…"))
				continue
			}
			lines = append(lines, m.tableRow1(b.Items[i], titleW, cols, focused && i == m.tableRow))
		}
	}
	if b.Rows > shown {
		lines = append(lines, dim.Render(scrollLine(off, shown, b.Rows, inner)))
	}

	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#30363d")).
		Padding(0, 1).
		Width(w - 2)
	if focused {
		border = border.Border(lipgloss.ThickBorder()).BorderForeground(accent)
	}
	return border.Render(strings.Join(lines, "\n"))
}

// tableRow1 draws one row: a card's bar in its Status colour, the change
// mark, then a cell per column. The selected row brightens like a card.
func (m model) tableRow1(it Item, titleW int, cols []tableCol, sel bool) string {
	color := m.rowColor(it)
	bar := lipgloss.NewStyle().Foreground(color).Render("▌")
	titleSt, cellSt := bold, ftLabel
	if sel {
		bar = lipgloss.NewStyle().Foreground(selBar).Render("▌")
		titleSt = bold.Foreground(lipgloss.Color("#ffffff"))
		cellSt = lipgloss.NewStyle().Foreground(selText)
	}
	mark := " "
	if t, ok := m.changedAt[it.ID]; ok && time.Since(t) < flashFor {
		mark = flash.Render("●")
	}
	row := bar + mark + titleSt.Render(fitCell(it.Title, titleW))
	for _, c := range cols {
		cell := fitCell(m.cellText(it, c.f), c.w)
		if rest, ok := strings.CutPrefix(cell, "●"); ok && c.f.Name == m.groupField { // the Status dot takes the column's colour
			row += strings.Repeat(" ", tableGap) + lipgloss.NewStyle().Foreground(color).Render("●") + cellSt.Render(rest)
			continue
		}
		row += strings.Repeat(" ", tableGap) + cellSt.Render(cell)
	}
	return row
}

// scrollLine says where a scrolled list is: ▲ hidden above · position · ▼
// hidden below, spread over w.
func scrollLine(off, vis, total, w int) string {
	up, down := off, total-off-vis
	pos := fmt.Sprintf("%d–%d of %d", off+1, off+vis, total)
	if vis == 1 {
		pos = fmt.Sprintf("%d of %d", off+1, total)
	}
	l, r := "  ", "  "
	if up > 0 {
		l = fmt.Sprintf("▲ %d", up)
	}
	if down > 0 {
		r = fmt.Sprintf("%d ▼", down)
	}
	gap := max(1, w-len([]rune(l))-len([]rune(r))-len([]rune(pos)))
	return l + strings.Repeat(" ", gap/2) + pos + strings.Repeat(" ", gap-gap/2) + r
}
