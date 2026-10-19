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
// On a short screen the boxes shrink (fewer cards each, then one line per
// card, then no cards at all) so the whole flow stays in view before grid
// rows start to scroll.

const (
	visibleCards = 3  // most cards shown per column; the rest scroll
	minBoxW      = 30 // narrowest a column box may get before wrapping
	gapW         = 5  // horizontal connector between boxes
	cardLines    = 3
	animEvery    = 150 * time.Millisecond
)

// boxLayout is how much of each column a box shows: vis cards, each in full
// (title, repo, labels) or compact (just the title line).
type boxLayout struct {
	vis     int
	compact bool
}

// layouts from roomiest to smallest. One full card is left out: three
// compact ones take the same height.
var layouts = []boxLayout{{3, false}, {2, false}, {3, true}, {2, true}, {1, true}, {0, true}}

// innerH is a box's inner height. Full: header + rule + cards (with a blank
// line between) + scroll line. Compact: header (whose count shows the scroll
// position) + a line per card. With no cards it's just the header.
func (l boxLayout) innerH() int {
	switch {
	case l.vis == 0:
		return 1
	case l.compact:
		return 1 + l.vis
	}
	return 2 + l.vis*cardLines + (l.vis - 1) + 1
}

// layoutFit is the roomiest layout that fits all rows of a grid in h lines,
// or headers only when none does.
func layoutFit(h, rows int) boxLayout {
	for _, l := range layouts {
		if rows*(l.innerH()+2)+(rows-1) <= h {
			return l
		}
	}
	return layouts[len(layouts)-1]
}

// shownCards is how many cards per column the board on screen shows.
func (m model) shownCards() int {
	n := len(m.columns)
	fit := gridFit(m.width, n)
	return layoutFit(m.boardH(), (n+fit-1)/max(1, fit)).vis
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

func (m model) boardView(h int) string {
	n := len(m.columns)
	if n == 0 {
		return dim.Render("  no items match")
	}
	fit := gridFit(m.width, n)
	boxW := (m.width - (fit-1)*gapW) / fit
	rows := (n + fit - 1) / fit
	lay := layoutFit(h, rows)
	boxH := lay.innerH() + 2

	// how many grid rows fit; keep the selected column's row on screen
	rowH := boxH + 1 // +1 for the wrap connector under each row
	shown := clamp((h+1)/rowH, 1, rows)
	selRow := m.col / fit
	first := clamp(selRow-shown+1, 0, rows-shown)

	var out []string
	for r := first; r < first+shown; r++ {
		var parts []string
		for c := 0; c < fit; c++ {
			i := r*fit + c
			if i >= n {
				break
			}
			if c > 0 {
				parts = append(parts, m.hFlow(boxH, i-1, m.columns[i].color))
			}
			parts = append(parts, m.columnBox(i, boxW, lay))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
		if r < rows-1 {
			out = append(out, m.wrapFlow(fit, boxW, (r+1)*fit-1, m.columns[(r+1)*fit].color))
		}
	}
	if hidden := rows - shown; hidden > 0 {
		hint := "↑↓ past a column's end moves between rows"
		if lay.vis == 0 {
			hint = "↑↓ moves between rows"
		}
		out = append(out, dim.Render(fmt.Sprintf(" grid rows %d–%d of %d · %s", first+1, first+shown, rows, hint)))
	}
	return strings.Join(out, "\n")
}

func (m model) columnBox(ci, w int, lay boxLayout) string {
	vis := lay.vis
	c := m.columns[ci]
	sel := ci == m.col
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
			card := strings.TrimSuffix(m.cardView(c.items[i], inner, sel && i == m.row), "\n")
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
	if c.total == 0 {
		// an empty column has no scroll line: centre the scene in everything
		// between the title rule and the bottom border (the pet only when it
		// fits whole)
		if m.settings.Pets && m.petFits(inner, ih-len(top)) {
			body = m.emptyState(ci, c, inner, ih-len(top), sel)
		} else {
			body = noTask(inner, ih-len(top)) // the default
		}
	}

	// scroll indicator: ▲ hidden above · position · ▼ hidden below
	var scroll string
	if c.total > vis {
		up, down := off, c.total-off-vis
		pos := fmt.Sprintf("%d–%d of %d", off+1, off+vis, c.total)
		if vis == 1 {
			pos = fmt.Sprintf("%d of %d", off+1, c.total)
		}
		l, r := "  ", "  "
		if up > 0 {
			l = fmt.Sprintf("▲ %d", up)
		}
		if down > 0 {
			r = fmt.Sprintf("%d ▼", down)
		}
		gap := max(1, inner-len([]rune(l))-len([]rune(r))-len([]rune(pos)))
		scroll = dim.Render(l + strings.Repeat(" ", gap/2) + pos + strings.Repeat(" ", gap-gap/2) + r)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, append(top, body)...)
	if c.total == 0 || lay.compact {
		content = lipgloss.NewStyle().Height(ih).MaxHeight(ih).Render(content)
	} else {
		content = lipgloss.NewStyle().Height(ih-1).MaxHeight(ih-1).Render(content) + "\n" + scroll
	}
	return border.Render(content)
}

// marching renders a dotted lane of width w whose bright dots move toward dir (+1 right, -1 left).
func (m model) marching(w, dir int, color string) string {
	lit := lipgloss.NewStyle().Foreground(ghColor(color)).Bold(true)
	var b strings.Builder
	for i := 0; i < w; i++ {
		if ((i-dir*m.frame)%3+3)%3 == 0 {
			b.WriteString(lit.Render("•"))
		} else {
			b.WriteString(dim.Render("·"))
		}
	}
	return b.String()
}

// move is a card changing column, kept for flashFor so the connectors it crossed light up.
type move struct {
	from, to string
	at       time.Time
}

func (m *model) pruneMoves() {
	kept := m.moves[:0]
	for _, mv := range m.moves {
		if time.Since(mv.at) < flashFor {
			kept = append(kept, mv)
		}
	}
	m.moves = kept
}

var (
	fwdColor  = "#e3b341" // card moved forward through the workflow
	backColor = "#db6d28" // card moved back (e.g. reopened, failed QA)
)

// traffic reports recent moves across connector k (between columns k and k+1):
// how many, and the direction of the most recent one (+1 forward, -1 back).
func (m model) traffic(k int) (n, dir int) {
	idx := make(map[string]int, len(m.columns))
	for i, c := range m.columns {
		idx[c.name] = i
	}
	var last time.Time
	for _, mv := range m.moves {
		a, okA := idx[mv.from]
		b, okB := idx[mv.to]
		if !okA || !okB || time.Since(mv.at) >= flashFor || k < min(a, b) || k >= max(a, b) {
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

// busyDots renders a lit lane: the idle dotted lane in bold, its dots big
// and racing twice as fast toward dir.
func (m model) busyDots(w, dir int, color string) string {
	hi := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	lo := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	var b strings.Builder
	for i := 0; i < w; i++ {
		if ((i-2*dir*m.frame)%3+3)%3 == 0 {
			b.WriteString(hi.Render("●"))
		} else {
			b.WriteString(lo.Render("•"))
		}
	}
	return b.String()
}

// hFlow is the connector between columns k and k+1 on the same grid row.
// Idle it's a dotted lane toward the next column; after a card crosses it, it
// lights up as bold racing dots (in the move's direction) with a count above.
func (m model) hFlow(h, k int, toColor string) string {
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", gapW)
	}
	n, dir := m.traffic(k)
	if n == 0 {
		head := lipgloss.NewStyle().Foreground(ghColor(toColor)).Render("▸")
		lines[h/2] = m.marching(gapW-1, 1, toColor) + head
		return strings.Join(lines, "\n")
	}
	color := fwdColor
	if dir < 0 {
		color = backColor
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	if dir > 0 {
		lines[h/2] = m.busyDots(gapW-1, 1, color) + st.Render("▸")
	} else {
		lines[h/2] = st.Render("◂") + m.busyDots(gapW-1, -1, color)
	}
	lines[h/2-1] = st.Render(lipgloss.PlaceHorizontal(gapW, lipgloss.Center, fmt.Sprintf("×%d", n)))
	return strings.Join(lines, "\n")
}

// wrapFlow is the connector from the last box of a grid row (column k) back to
// the first box of the next row: ╯ under the last box, a lane running left, ╭
// down into the next. It lights up like hFlow when cards cross it.
func (m model) wrapFlow(fit, boxW, k int, toColor string) string {
	x0 := boxW / 2
	n, dir := m.traffic(k)
	color := ghColors["GRAY"]
	if n > 0 {
		color = fwdColor
		if dir < 0 {
			color = backColor
		}
	}
	if fit == 1 {
		arrow := "▼"
		if dir < 0 {
			arrow = "▲"
		}
		if m.frame%4 >= 2 {
			arrow = map[string]string{"▼": "▽", "▲": "△"}[arrow]
		}
		st := lipgloss.NewStyle().Foreground(ghColor(toColor))
		label := ""
		if n > 0 {
			st = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
			label = st.Render(fmt.Sprintf(" ×%d", n))
		}
		return strings.Repeat(" ", x0) + st.Render(arrow) + label
	}
	x1 := (fit-1)*(boxW+gapW) + boxW/2
	w := x1 - x0 - 1
	if n == 0 {
		edge := lipgloss.NewStyle().Foreground(ghColor(toColor))
		return strings.Repeat(" ", x0) + edge.Render("╭") + m.marching(w, -1, toColor) + dim.Render("╯")
	}
	// The lane runs right→left (forward). A backward move travels it left→right.
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
	label := fmt.Sprintf(" ×%d ", n)
	lw := len([]rune(label))
	left := (w - lw) / 2
	return strings.Repeat(" ", x0) + st.Render("╭") +
		m.busyDots(left, -dir, color) + st.Render(label) + m.busyDots(w-lw-left, -dir, color) + st.Render("╯")
}
