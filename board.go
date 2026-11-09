package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Board layout: columns sit side by side while they fit and wrap into further
// grid rows when they don't, in workflow order (left→right, then next row).
// Animated dotted connectors between boxes show the direction work flows.
// Every grid row gets the same height, together filling the screen; each box
// shows as many cards as fit in it (full while two fit, then one line per
// card, then none) so the whole flow stays in view before grid rows start
// to scroll.

const (
	visibleCards = 3  // cards a box tries to show in full before going one line per card; also the minimum loaded
	minBoxW      = 30 // narrowest a column box may get before wrapping
	gapW         = 5  // horizontal connector between boxes
	cardLines    = 3
	animEvery    = 150 * time.Millisecond
)

// boxLayout is how much of each column a box shows: vis cards, each in full
// (title, repo, labels) or compact (just the title line), plus fill blank
// lines when a stretched box is taller than its cards.
type boxLayout struct {
	vis     int
	compact bool
	fill    int
}

// innerH is a box's inner height. Full: header + rule + cards (with a blank
// line between) + scroll line. Compact: header (whose count shows the scroll
// position) + a line per card. With no cards it's just the header.
func (l boxLayout) innerH() int {
	switch {
	case l.vis == 0:
		return 1 + l.fill
	case l.compact:
		return 1 + l.vis + l.fill
	}
	return 2 + l.vis*cardLines + (l.vis - 1) + 1 + l.fill
}

// sized is the layout for a box inner lines tall whose column (the fullest
// of its grid row) has need cards, at most visibleCards: full cards when at
// least two (or its only one) fit, else one line per card; "No Task" for a
// row of empty columns. Leftover lines stay blank.
func sized(inner, need int) boxLayout {
	if need == 0 { // "No Task" fills it; with room, under a title rule like a full box
		if full := (boxLayout{vis: 1}); inner >= full.innerH() {
			return boxLayout{vis: 1, fill: inner - full.innerH()}
		}
		return boxLayout{vis: max(0, inner-1), compact: true}
	}
	if vis := (inner - 2) / (cardLines + 1); vis >= max(1, min(need, 2)) { // a card and the blank line after it; the scroll line takes the last one's
		l := boxLayout{vis: vis}
		l.fill = inner - l.innerH()
		return l
	}
	return boxLayout{vis: max(0, inner-1), compact: true}
}

// gridH is how tall grid rows with these layouts are, with the wrap
// connector between rows.
func gridH(lays []boxLayout) int {
	h := max(0, len(lays)-1)
	for _, l := range lays {
		h += l.innerH() + 2
	}
	return h
}

// rowLayouts lays out a grid's rows in a board h lines tall. Every row
// gets the same height, filling the board, and each box shows as many cards
// as fit in it. When even the column headers don't fit, it's headers only
// and the rows scroll.
func rowLayouts(cols []column, fit, h int) []boxLayout {
	rows := (len(cols) + fit - 1) / max(1, fit)
	need := make([]int, rows)
	for i, c := range cols {
		need[i/fit] = max(need[i/fit], min(c.total, visibleCards))
	}
	inner := max(1, (h+1)/max(1, rows)-3) // each row: its box (inner + 2 borders) and the connector under it
	lays := make([]boxLayout, rows)
	for r := range lays {
		lays[r] = sized(inner, need[r])
	}
	return lays
}

// shownCards is how many cards the selected column's box shows.
func (m model) shownCards() int {
	return m.colCards(m.col)
}

// colCards is how many cards column i's box has room for.
func (m model) colCards(i int) int {
	n := len(m.columns)
	if n == 0 {
		return 0
	}
	w, h := m.boardSize()
	fit := gridFit(w, n)
	lays := rowLayouts(m.columns, fit, h)
	return lays[clamp(i, 0, n-1)/fit].vis
}

type animMsg struct{}

func animate() tea.Cmd {
	return tea.Tick(animEvery, func(time.Time) tea.Msg { return animMsg{} })
}

// gridFit is how many column boxes fit side by side in width.
func gridFit(width, n int) int {
	if n == 0 {
		return 1
	}
	return clamp((width+gapW)/(minBoxW+gapW), 1, n)
}

// boardView draws the board's columns in a w×h area.
func (m model) boardView(w, h int) string {
	n := len(m.columns)
	if n == 0 {
		return dim.Render("  no items match")
	}
	fit := gridFit(w, n)
	boxW := (w - (fit-1)*gapW) / fit
	rows := (n + fit - 1) / fit
	lays := rowLayouts(m.columns, fit, h)

	// which grid rows fit, keeping the selected column's row on screen
	selRow := min(m.col/fit, rows-1)
	first := 0
	for first < selRow && gridH(lays[first:selRow+1]) > h {
		first++
	}
	shown := 1
	for first+shown < rows && gridH(lays[first:first+shown+1]) <= h {
		shown++
	}

	var out []string
	for r := first; r < first+shown; r++ {
		lay := lays[r]
		boxH := lay.innerH() + 2
		var parts []string
		for c := 0; c < fit; c++ {
			i := r*fit + c
			if i >= n {
				break
			}
			if c > 0 {
				parts = append(parts, m.hFlow(boxH, i-1, ghColor(m.columns[i].color)))
			}
			parts = append(parts, m.columnBox(i, boxW, lay))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
		if r < rows-1 {
			out = append(out, m.wrapFlow(fit, boxW, (r+1)*fit-1, ghColor(m.columns[(r+1)*fit].color)))
		}
	}
	if hidden := rows - shown; hidden > 0 {
		hint := "↑↓ past a column's end moves between rows"
		if lays[selRow].vis == 0 {
			hint = "↑↓ moves between rows"
		}
		out = append(out, dim.Render(fmt.Sprintf(" grid rows %d–%d of %d · %s", first+1, first+shown, rows, hint)))
	}
	return strings.Join(out, "\n")
}

func (m model) columnBox(ci, w int, lay boxLayout) string {
	vis := lay.vis
	c := m.columns[ci]
	// the unfocused board of a split has no cursor
	sel := ci == m.col && !m.bg
	inner := w - 4 // border + padding

	color := ghColor(c.color)
	nameSt := bold
	if sel {
		nameSt = bold.Foreground(color)
	}
	count := dim.Render(fmt.Sprint(c.total))
	if lay.compact && lay.vis > 0 && c.total > lay.vis { // no scroll line: the count says where
		off := clamp(m.offsets[c.name], 0, max(0, min(c.total, 100)-lay.vis))
		pos := fmt.Sprint(off + 1)
		if lay.vis > 1 {
			pos += fmt.Sprintf("–%d", min(off+lay.vis, c.total))
		}
		count = dim.Render(pos + "/" + fmt.Sprint(c.total))
	}
	name := lipgloss.NewStyle().Foreground(color).Render("●") + " " +
		nameSt.Render(trunc(c.name, inner-lipgloss.Width(count)-3))
	head := name + strings.Repeat(" ", max(1, inner-lipgloss.Width(name)-lipgloss.Width(count))) + count

	ruleCh := "─"
	if sel {
		ruleCh = "━"
	}
	rule := lipgloss.NewStyle().Foreground(color).Faint(!sel).Render(strings.Repeat(ruleCh, inner))

	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#30363d")).
		Padding(0, 1).
		Width(w - 2)
	if sel {
		border = border.Border(lipgloss.ThickBorder()).BorderForeground(color)
	}
	if vis == 0 { // too short for cards: just the column and its count
		return border.Render(head)
	}

	// windowed boards load at most the first 100 cards of a column
	reach := min(c.total, 100)
	off := clamp(m.offsets[c.name], 0, max(0, reach-vis))
	var cards []string
	for i := off; i < off+vis; i++ {
		switch {
		case i < len(c.items):
			card := strings.TrimSuffix(m.cardView(c.items[i], inner, sel && i == m.row, color), "\n")
			if lay.compact { // the title line only
				card, _, _ = strings.Cut(card, "\n")
			}
			cards = append(cards, card)
		case i < reach: // scrolled onto cards the next sync is fetching
			cards = append(cards, " "+m.spin.View()+dim.Render(" loading…")+strings.Repeat("\n", pick(lay.compact, 0, cardLines-1)))
		default:
			cards = append(cards, strings.Repeat("\n", pick(lay.compact, 0, cardLines-1)))
		}
	}
	body := strings.Join(cards, pick(lay.compact, "\n", "\n\n"))
	ih := lay.innerH()
	top := []string{head, rule}
	if lay.compact { // no title rule: every line goes to cards
		top = top[:1]
	}
	if c.total == 0 { // no scroll line: "No Task" fills everything below the title rule
		body = noTask(inner, ih-len(top))
	}

	// scroll indicator: ▲ hidden above · position · ▼ hidden below
	var scroll string
	if c.total > vis {
		scroll = dim.Render(scrollLine(off, vis, c.total, inner))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, append(top, body)...)
	if c.total == 0 || lay.compact {
		content = lipgloss.NewStyle().Height(ih).MaxHeight(ih).Render(content)
	} else {
		content = lipgloss.NewStyle().Height(ih-1).MaxHeight(ih-1).Render(content) + "\n" + scroll
	}
	return border.Render(content)
}

// marching renders a dotted lane of width w whose bright dots move toward dir
// (+1 right, -1 left). With head given, the dot at that index is white.
func (m model) marching(w, dir int, color lipgloss.Color, head ...int) string {
	lit := lipgloss.NewStyle().Foreground(color).Bold(true)
	var b strings.Builder
	for i := 0; i < w; i++ {
		if len(head) > 0 && i == head[0] {
			b.WriteString(whiteDot.Render("•"))
		} else if ((i-dir*m.frame)%3+3)%3 == 0 {
			b.WriteString(lit.Render("•"))
		} else {
			b.WriteString(dim.Render("·"))
		}
	}
	return b.String()
}

// move is a card changing column. The connectors it crossed stay lit for the
// rest of the session (until the project or view changes).
type move struct {
	from, to string
	at       time.Time
}

// pruneMoves keeps only the latest move for each from→to pair: that's all the
// connectors need, and it keeps a long session's list small.
func (m *model) pruneMoves() {
	latest := map[[2]string]int{}
	kept := m.moves[:0]
	for _, mv := range m.moves {
		key := [2]string{mv.from, mv.to}
		if i, ok := latest[key]; ok {
			if mv.at.After(kept[i].at) {
				kept[i] = mv
			}
			continue
		}
		latest[key] = len(kept)
		kept = append(kept, mv)
	}
	m.moves = kept
}

// traffic reports moves across connector k (between columns k and k+1) this
// session: how many kinds, and the direction of the most recent one (+1
// forward, -1 back).
func (m model) traffic(k int) (n, dir int) {
	idx := make(map[string]int, len(m.columns))
	for i, c := range m.columns {
		idx[c.name] = i
	}
	var last time.Time
	for _, mv := range m.moves {
		a, okA := idx[mv.from]
		b, okB := idx[mv.to]
		if !okA || !okB || k < min(a, b) || k >= max(a, b) {
			continue
		}
		n++
		if !mv.at.Before(last) {
			last, dir = mv.at, 1
			if b < a {
				dir = -1
			}
		}
	}
	return n, dir
}

// whiteDot is the dot streaming along a lane a card just crossed.
var whiteDot = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)

// busyDots renders a lit lane: the idle dotted lane with one white dot
// streaming along it toward dir. When it reaches the end the next one starts
// over; long lanes stream faster so every loop takes about as long.
func (m model) busyDots(w, dir int, color lipgloss.Color) string {
	if w <= 0 {
		return ""
	}
	pos := m.frame * max(1, w/8) % w
	if dir < 0 {
		pos = w - 1 - pos
	}
	return m.marching(w, dir, color, pos)
}

// hFlow is the connector between columns k and k+1 on the same grid row.
// Idle it's a dotted lane toward the next column; after a card crosses it, a
// white dot streams along it in the move's direction.
func (m model) hFlow(h, k int, toColor lipgloss.Color) string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", gapW)
	}
	head := lipgloss.NewStyle().Foreground(toColor)
	switch n, dir := m.traffic(k); {
	case n == 0:
		lines[h/2] = m.marching(gapW-1, 1, toColor) + head.Render("▸")
	case dir > 0:
		lines[h/2] = m.busyDots(gapW-1, 1, toColor) + head.Render("▸")
	default:
		lines[h/2] = head.Render("◂") + m.busyDots(gapW-1, -1, toColor)
	}
	return strings.Join(lines, "\n")
}

// wrapFlow is the connector from the last box of a grid row (column k) back to
// the first box of the next row: ╯ under the last box, a lane running left, ╭
// down into the next. It streams like hFlow when cards cross it.
func (m model) wrapFlow(fit, boxW, k int, toColor lipgloss.Color) string {
	x0 := boxW / 2
	n, dir := m.traffic(k)
	edge := lipgloss.NewStyle().Foreground(toColor)
	if fit == 1 {
		arrow := "▼"
		if dir < 0 {
			arrow = "▲"
		}
		if m.frame%4 >= 2 {
			arrow = map[string]string{"▼": "▽", "▲": "△"}[arrow]
		}
		return strings.Repeat(" ", x0) + edge.Bold(n > 0).Render(arrow)
	}
	x1 := (fit-1)*(boxW+gapW) + boxW/2
	w := x1 - x0 - 1
	lane := m.marching(w, -1, toColor)
	if n > 0 { // the lane runs right→left (forward); a backward move travels it left→right
		lane = m.busyDots(w, -dir, toColor)
	}
	return strings.Repeat(" ", x0) + edge.Render("╭") + lane + dim.Render("╯")
}

// noTask is an empty column: "No Task" over a faint grid
// that fills the space from the title rule down to the bottom border.
func noTask(w, h int) string {
	grid := lipgloss.NewStyle().Foreground(lipgloss.Color("#21262d"))
	cross, bar := gridRow(w, "┼", "─"), gridRow(w, "│", " ")
	label := " No Task "
	mid := h / 2
	lines := make([]string, h)
	for y := range lines {
		row := bar
		if y%2 == 0 {
			row = cross
		}
		if y != mid {
			lines[y] = grid.Render(row)
			continue
		}
		left := (w - len(label)) / 2
		r := []rune(row)
		lines[y] = grid.Render(string(r[:left])) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Render(label) +
			grid.Render(string(r[left+len(label):]))
	}
	return strings.Join(lines, "\n")
}

// gridRow repeats a 4-wide grid cell (a joint then 3 fill characters) across w.
func gridRow(w int, joint, fill string) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		if x%4 == 0 {
			b.WriteString(joint)
		} else {
			b.WriteString(fill)
		}
	}
	return b.String()
}
