package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// footBox is one titled section of the footer: ╭─ Title ───╮ │ lines │ ╰───╯
// alts are ever shorter one-line versions of its first line (short words,
// then icons), so boxes can stay side by side on a narrow screen.
type footBox struct {
	title  string
	accent lipgloss.Color // title colour
	lines  []string
	alts   []string
}

func (b footBox) natural() int {
	w := lipgloss.Width(b.title) + 3
	for _, l := range b.lines {
		w = max(w, lipgloss.Width(l))
	}
	return w + 4 // borders + padding
}

// short is the box with its first line swapped for alt i (0 is the original).
func (b footBox) short(i int) footBox {
	if i == 0 || i > len(b.alts) || len(b.lines) == 0 {
		return b
	}
	lines := append([]string{b.alts[i-1]}, b.lines[1:]...)
	return footBox{title: b.title, accent: b.accent, lines: lines}
}

// narrowest is the box at its shortest.
func (b footBox) narrowest() int { return b.short(len(b.alts)).natural() }

// render draws the box w wide and h content lines tall, truncating lines that don't fit.
func (b footBox) render(w, h int) string {
	inner := max(1, w-4)
	bc := lipgloss.NewStyle().Foreground(lipgloss.Color("#30363d"))
	title := lipgloss.NewStyle().Foreground(b.accent).Bold(true).Render(" " + b.title + " ")
	top := bc.Render("╭─") + title + bc.Render(strings.Repeat("─", max(0, w-3-lipgloss.Width(title)))+"╮")
	out := []string{top}
	for i := 0; i < h; i++ {
		l := ""
		if i < len(b.lines) {
			l = lipgloss.NewStyle().MaxWidth(inner).Render(b.lines[i])
		}
		out = append(out, bc.Render("│")+" "+l+strings.Repeat(" ", max(0, inner-lipgloss.Width(l)))+" "+bc.Render("│"))
	}
	out = append(out, bc.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
}

// boxRows packs boxes into rows that fit width (in order, as many per row as
// fit at their shortest), shortens the widest boxes in a row until it fits,
// then stretches each row to the full width and evens out the heights.
func boxRows(boxes []footBox, width int) string {
	const gap = 1
	var rows [][]footBox
	var row []footBox
	used := 0
	for _, b := range boxes {
		w := min(b.narrowest(), width)
		if len(row) > 0 && used+gap+w > width {
			rows, row, used = append(rows, row), nil, 0
		}
		if len(row) > 0 {
			used += gap
		}
		row, used = append(row, b), used+w
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	var out []string
	for _, r := range rows {
		r = shorten(r, width, gap)
		natural, h := 0, 1
		for _, b := range r {
			natural += min(b.natural(), width)
			h = max(h, len(b.lines))
		}
		extra := max(0, width-natural-gap*(len(r)-1))
		var parts []string
		for i, b := range r {
			w := min(b.natural(), width) + extra/len(r)
			if i == len(r)-1 {
				w += extra % len(r)
			}
			if i > 0 {
				parts = append(parts, strings.Repeat(" ", gap))
			}
			parts = append(parts, b.render(w, h))
		}
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}
	return strings.Join(out, "\n")
}

// shorten swaps the widest boxes of a row for shorter versions until the row fits.
func shorten(r []footBox, width, gap int) []footBox {
	level := make([]int, len(r))
	out := append([]footBox(nil), r...)
	for {
		total, widest := gap*(len(r)-1), -1
		for i, b := range out {
			total += b.natural()
			if level[i] < len(r[i].alts) && (widest < 0 || b.natural() > out[widest].natural()) {
				widest = i
			}
		}
		if total <= width || widest < 0 {
			return out
		}
		level[widest]++
		out[widest] = r[widest].short(level[widest])
	}
}
