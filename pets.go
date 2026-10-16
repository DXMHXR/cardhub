package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Empty columns get a little pet instead of words: a different creature per
// column, drawn in the column's colour, idling (blinking, wiggling, bobbing).
// Each column's pet runs on its own phase so they don't move in lockstep.

// pet draws one frame. t advances every ~600ms; blink is true briefly every
// few seconds.
type pet func(t int, blink bool) []string

func eyes(blink bool, open string) string {
	if blink {
		return "─"
	}
	return open
}

var pets = []pet{
	// blob, shuffling its feet
	func(t int, b bool) []string {
		e := eyes(b, "●")
		feet := []string{" ▀ ▀ ▀ ", "▀ ▀ ▀ ▀"}[t%2]
		return []string{" ▄███▄ ", "█ " + e + " " + e + " █", "█  ▾  █", feet}
	},
	// ghost, waving its tail
	func(t int, b bool) []string {
		e := eyes(b, "°")
		tail := []string{"╰v^v^╯", "╰^v^v╯"}[t%2]
		return []string{"╭────╮", "│" + e + "  " + e + "│", "│ ◡◡ │", tail}
	},
	// cat, twitching an ear
	func(t int, b bool) []string {
		e := eyes(b, "o")
		ears := []string{" /\\_/\\ ", " /|_/\\ "}[t%4/3]
		return []string{ears, "( " + e + "." + e + " )", " > ^ < ", []string{"  ╯ ╰~ ", "  ╯ ╰ ~"}[t%2]}
	},
	// space invader, marching
	func(t int, b bool) []string {
		legs := []string{" ▀    ▀ ", "▀      ▀"}[t%2]
		return []string{" ▄▀▄▄▀▄ ", "▄█▀██▀█▄", "▀▀█▀▀█▀▀", legs}
	},
	// octopus, swimming
	func(t int, b bool) []string {
		e := eyes(b, "◕")
		legs := []string{"╱╲╱╲╱╲", "╲╱╲╱╲╱"}[t%2]
		return []string{" ▄▄▄▄ ", "█" + e + "  " + e + "█", "▀█▀▀█▀", legs}
	},
	// robot, antenna light blinking
	func(t int, b bool) []string {
		e := eyes(b, "°")
		light := []string{"  ╷  ", "  •  "}[t%2]
		return []string{light, "╭─┴─╮", "│" + e + "_" + e + "│", "╰┬─┬╯"}
	},
	// sleepy slime, dreaming
	func(t int, b bool) []string {
		z := []string{"      z ", "     z  ", "    Z   ", "        "}[t%4]
		return []string{z, " ▄▄▄▄▄  ", "█ ─ ─ █ ", "▀▀▀▀▀▀▀ "}
	},
	// chick, hopping from foot to foot
	func(t int, b bool) []string {
		e := eyes(b, "•")
		feet := []string{"  ╯ ╰  ", "  ╰ ╯  "}[t%2]
		return []string{"  ▄█▄  ", " (" + e + " " + e + ") ", " ( > ) ", feet}
	},
}

// emptyState draws column ci's pet (or a puzzled one with "no match" while a
// search finds nothing there), centred in the column's card area.
func (m model) emptyState(ci int, c column, inner int, sel bool) string {
	h := visibleCards*cardLines + visibleCards - 1
	f := m.frame + ci*7 // each column on its own phase
	t := f / 4          // ~600ms per step
	blink := f%27 < 2   // a quick blink every ~4s
	art := pets[ci%len(pets)](t, blink)

	// pad to a block so the creature stays intact when centred
	w := 0
	for _, l := range art {
		w = max(w, lipgloss.Width(l))
	}
	style := lipgloss.NewStyle().Foreground(ghColor(c.color)).Faint(!sel)
	var lines []string
	if (t/2)%2 == 1 { // a gentle bob
		lines = append(lines, "")
	}
	for _, l := range art {
		lines = append(lines, style.Render(l+strings.Repeat(" ", w-lipgloss.Width(l))))
	}
	if strings.TrimSpace(m.filter.Value()) != "" {
		lines = append(lines, "", dim.Render("no match"))
	}
	for i, l := range lines {
		lines[i] = lipgloss.PlaceHorizontal(inner, lipgloss.Center, l)
	}
	return lipgloss.Place(inner, h, lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}
