package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	lvGrey   = lipgloss.Color("#8b949e")
	lvGreen  = lipgloss.Color("#3fb950")
	lvYellow = lipgloss.Color("#d29922")
	lvBlue   = lipgloss.Color("#58a6ff")
	lvPurple = lipgloss.Color("#a371f7")
	lvRed    = lipgloss.Color("#f85149")
	lvWhite  = lipgloss.Color("#f0f6fc")
)

func fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// linkView is the AI link screen (tab 3): who's watching and listening, the
// numbers at a glance, the rules agents set, and a timeline of what happened.
// Wide terminals get the timeline in a column of its own.
func (m model) linkView(h int) string {
	snap := m.notifier.Snapshot()
	twoCol := m.width >= 140
	leftW := m.width
	if twoCol {
		leftW = m.width * 11 / 20
	}

	var left []string
	left = append(left, m.linkHero(snap, leftW))
	if snap.Active {
		left = append(left, linkTiles(snap, leftW), m.linkRules(snap, leftW))
		if len(snap.Rules) == 0 && snap.Sent == 0 {
			left = append(left, m.linkConnect(leftW))
		}
	}
	leftCol := strings.Join(left, "\n")

	if twoCol {
		rightW := m.width - leftW - 1
		right := m.linkActivity(snap, rightW, h-2)
		return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, " ", right)
	}
	return leftCol + "\n" + m.linkActivity(snap, m.width, h-lipgloss.Height(leftCol)-2)
}

// linkHero: the board being watched and whether an agent is listening.
func (m model) linkHero(snap LinkSnapshot, w int) string {
	inner := w - 4
	switch {
	case snap.Conflict != "":
		return boxRows([]footBox{{title: "AI link", accent: lvYellow, lines: []string{
			fg(lvYellow).Render("! ") + ftLabel.Render(snap.Conflict),
			dim.Render("Close the other cardhub on this board to let this one serve AI agents."),
		}}}, w)
	case !snap.Active:
		return boxRows([]footBox{{title: "AI link", accent: lvGrey, lines: []string{
			ftLabel.Render("Not serving yet: pick a project first."),
		}}}, w)
	}

	name := bold.Foreground(lvWhite).Render(snap.Title)
	if snap.Title == "" {
		name = bold.Foreground(lvWhite).Render(snap.Board)
	} else {
		name += dim.Render("  " + snap.Board)
	}
	status := fg(lvGrey).Render("○ no agent waiting")
	accent := lvGrey
	if snap.Waiting {
		status, accent = bold.Foreground(lvGreen).Render("● agent waiting"), lvGreen
	}
	gap := max(2, inner-lipgloss.Width(name)-lipgloss.Width(status))
	line1 := name + strings.Repeat(" ", gap) + status

	path := snap.Path
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	line2 := dim.Render("listening since "+snap.Since.Format("15:04")) + dim.Render(" · ") + dim.Render(trunc(path, max(10, inner-26)))
	lines := []string{line1, line2}
	if snap.Paused {
		accent = lvYellow
		lines = append(lines, fg(lvYellow).Bold(true).Render("❚❚ Syncing paused")+
			dim.Render(": rules aren't checked and nothing new is reported. Space resumes."))
	}
	return boxRows([]footBox{{title: "Watching", accent: accent, lines: lines}}, w)
}

// linkTiles: the four numbers that matter, as tiles.
func linkTiles(snap LinkSnapshot, w int) string {
	matching := 0
	for _, r := range snap.Rules {
		matching += r.Matches
	}
	tile := func(title string, n int, unit string, c lipgloss.Color) footBox {
		if n == 0 {
			c = lvGrey
		}
		return footBox{title: title, accent: lvGrey, lines: []string{bold.Foreground(c).Render(fmt.Sprint(n)) + dim.Render(" "+unit)}}
	}
	return boxRows([]footBox{
		tile("Rules", len(snap.Rules), "active", lvBlue),
		tile("Matching", matching, "cards", lvWhite),
		tile("Queued", snap.Queued, "to send", lvYellow),
		tile("Sent", snap.Sent, "total", lvGreen),
	}, w)
}

// linkRules: an aligned table of the rules with highlighted filters.
func (m model) linkRules(snap LinkSnapshot, w int) string {
	inner := w - 4
	title := fmt.Sprintf("Rules (%d)", len(snap.Rules))
	if len(snap.Rules) == 0 {
		return boxRows([]footBox{{title: title, accent: lvBlue, lines: []string{
			dim.Render("No rules yet. An agent adds one with ") + ftKey.Render("cardhub notify add '<filter>'"),
		}}}, w)
	}
	const numW = 9 // "MATCHING" / "REPORTED" columns
	filterW := max(10, inner-4-2*(numW+2))
	head := dim.Render(fmt.Sprintf("%-3s %-*s  %*s  %*s", "#", filterW, "FILTER", numW, "MATCHING", numW, "REPORTED"))
	lines := []string{head}
	for _, r := range snap.Rules {
		f := highlightFilter(trunc(r.Filter, filterW))
		pad := strings.Repeat(" ", max(0, filterW-lipgloss.Width(f)))
		lines = append(lines, ftKey.Render(fmt.Sprintf("%-3d", r.ID))+" "+f+pad+"  "+
			bold.Foreground(lvWhite).Render(fmt.Sprintf("%*d", numW, r.Matches))+"  "+
			fg(lvGreen).Render(fmt.Sprintf("%*d", numW, r.Reported)))
	}
	return boxRows([]footBox{{title: title, accent: lvBlue, lines: lines}}, w)
}

// linkConnect: how to hook an agent up, until one has.
func (m model) linkConnect(w int) string {
	inner := w - 4
	step := func(n, cmd, what string) string {
		return fg(lvBlue).Bold(true).Render(n) + "  " + ftKey.Render(cmd) + dim.Render("  "+what)
	}
	lines := []string{
		step("1", "cardhub notify add '<filter>'", "report cards matching a GitHub project filter"),
		step("2", "cardhub events --wait --json", "in the background: wakes the agent on each report"),
		dim.Render("   Quitting cardhub stops the watcher, and a waiting agent is told (exit code 3)."),
		"",
		ftLabel.Render("Or paste this sample prompt into your agent ") + dim.Render("(") + ftKey.Render("C") + dim.Render(" copies it):"),
	}
	for _, l := range wrapWords(samplePrompt(), max(10, inner-4)) {
		lines = append(lines, fg(lvBlue).Render("▎ ")+fg(lipgloss.Color("#c9d1d9")).Render(l))
	}
	lines = append(lines, dim.Render("With several boards open, add --board \"<project name>\" to both commands."))
	if m.copied != "" {
		lines = append(lines, fg(lvGreen).Render("✔ "+m.copied))
	}
	return boxRows([]footBox{{title: "Connect an agent", accent: lvGrey, lines: lines}}, w)
}

// linkActivity: a timeline, newest first, filling the space it's given.
func (m model) linkActivity(snap LinkSnapshot, w, h int) string {
	inner := w - 4
	rows := max(1, h-2)
	icons := map[string]struct {
		icon string
		c    lipgloss.Color
	}{
		"rule": {"✚", lvBlue}, "queued": {"◆", lvYellow}, "sent": {"➜", lvGreen},
		"agent": {"◉", lvPurple}, "stop": {"■", lvRed},
	}
	var lines []string
	if len(snap.Log) == 0 {
		lines = append(lines, dim.Render("Nothing yet."))
	}
	for i, l := range snap.Log {
		if i == rows {
			break
		}
		ic := icons[l.Kind]
		prefix := dim.Render(l.At.Format("15:04:05")) + "  " + fg(ic.c).Render(ic.icon) + "  "
		room := inner - lipgloss.Width(prefix)
		var text string
		if c := l.Card; c != nil {
			num := bold.Foreground(lvWhite).Render(fmt.Sprintf("#%d", c.Number))
			tag := fg(ghColor(m.columnColor(c.Status))).Render("[" + c.Status + "]")
			rule := dim.Render(fmt.Sprintf(" · rule %d", c.Rule))
			titleRoom := room - lipgloss.Width(num) - lipgloss.Width(tag) - lipgloss.Width(rule) - 2
			text = num + " " + tag + " " + ftLabel.Render(trunc(c.Title, max(4, titleRoom))) + rule
		} else {
			text = ftLabel.Render(trunc(l.Text, room))
		}
		lines = append(lines, prefix+text)
	}
	return boxRows([]footBox{{title: "Activity", accent: lvPurple, lines: lines}}, w)
}

// columnColor is the board colour of a column (a group-by option), for tags.
func (m model) columnColor(name string) string {
	for _, c := range m.columns {
		if c.name == name {
			return c.color
		}
	}
	return "GRAY"
}

// highlightFilter colours a GitHub project filter: "-" exclusions red,
// qualifier names blue, values green, free text white.
func highlightFilter(f string) string {
	var out []string
	for _, tok := range splitFilter(f) {
		var b strings.Builder
		if strings.HasPrefix(tok, "-") && len(tok) > 1 {
			b.WriteString(fg(lvRed).Bold(true).Render("−"))
			tok = tok[1:]
		}
		if k, v, ok := strings.Cut(tok, ":"); ok && k != "" && !strings.HasPrefix(k, `"`) {
			b.WriteString(fg(lvBlue).Render(k) + dim.Render(":") + fg(lipgloss.Color("#7ee787")).Render(v))
		} else {
			b.WriteString(fg(lvWhite).Render(tok))
		}
		out = append(out, b.String())
	}
	return strings.Join(out, " ")
}

// splitFilter splits on spaces outside double quotes.
func splitFilter(f string) []string {
	var toks []string
	var cur strings.Builder
	quoted := false
	for _, r := range f {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ' ' && !quoted:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

// samplePrompt is a starting point for an agent's instructions. It names no
// board (the commands use the only open one) and filters on GitHub's default
// "bug" label, so it works on any project as is.
func samplePrompt() string {
	return "Run `cardhub notify add 'is:open label:bug'`, then run `cardhub events --wait --json` in the background. " +
		"When it returns, handle each reported card, oldest first, then run `events --wait` again. " +
		"If it exits with code 3 or 4, cardhub was closed: stop and say so."
}

// wrapWords wraps text at spaces only, so flags like --json never split.
func wrapWords(text string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case lipgloss.Width(line)+1+lipgloss.Width(w) <= width:
			line += " " + w
		default:
			lines = append(lines, line)
			line = w
		}
	}
	return append(lines, line)
}
