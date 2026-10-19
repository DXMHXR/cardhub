package main

import (
	"fmt"
	"sort"
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

// linkView is the AI link screen (tab 3): every watcher (one per project AI
// agents use), the numbers at a glance, the rules, and a timeline of what
// happened. Wide terminals get the timeline in a column of its own.
func (m model) linkView(h int) string {
	snaps := m.hub.Snapshots()
	twoCol := m.width >= 140
	leftW := m.width
	if twoCol {
		leftW = m.width * 11 / 20
	}

	left := []string{m.linkWatchers(snaps, leftW)}
	rules, sent := 0, 0
	for _, s := range snaps {
		rules += len(s.Rules)
		sent += s.Sent
	}
	if len(snaps) > 0 {
		left = append(left, linkTiles(snaps, leftW), m.linkRules(snaps, leftW))
		if rules == 0 && sent == 0 {
			left = append(left, m.linkConnect(leftW))
		}
	}
	leftCol := strings.Join(left, "\n")

	if twoCol {
		right := m.linkActivity(snaps, m.width-leftW-1, h-2)
		return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, " ", right)
	}
	return leftCol + "\n" + m.linkActivity(snaps, m.width, h-lipgloss.Height(leftCol)-2)
}

// linkWatchers: one line per watcher, the project on screen first.
func (m model) linkWatchers(snaps []LinkSnapshot, w int) string {
	inner := w - 4
	st := m.hub.Status()
	var lines []string
	accent := lvGrey
	if st.Conflict != "" {
		accent = lvYellow
		lines = append(lines, fg(lvYellow).Render("! ")+ftLabel.Render(st.Conflict))
	}
	for _, s := range snaps {
		if !s.Active {
			continue
		}
		name := bold.Foreground(lvWhite).Render(firstNonEmpty(s.Title, s.Board))
		if s.Title != "" {
			name += dim.Render("  " + s.Board)
		}
		if s.OnScreen {
			name += "  " + fg(lvBlue).Render("on screen")
		}
		status := fg(lvGrey).Render("○ no agent")
		if s.Waiting {
			status, accent = bold.Foreground(lvGreen).Render("● agent waiting"), lvGreen
		}
		detail := dim.Render(fmt.Sprintf("%s · %d sent", plural(len(s.Rules), "rule"), s.Sent))
		if s.Queued > 0 {
			detail += fg(lvYellow).Render(fmt.Sprintf(" · %d queued", s.Queued))
		}
		right := detail + "   " + status
		gap := max(2, inner-lipgloss.Width(name)-lipgloss.Width(right))
		lines = append(lines, name+strings.Repeat(" ", gap)+right)
	}
	if st.Boards == 0 {
		lines = append(lines, ftLabel.Render("No watchers running. ")+
			dim.Render(`Open a project, or let an agent run cardhub notify add --board "<project name>".`))
	}
	if st.Boards > 0 {
		lines = append(lines, dim.Render("Switching project or pausing the board doesn't stop a watcher. ")+ftKey.Render("X")+dim.Render(" stops one."))
	}
	return boxRows([]footBox{{title: fmt.Sprintf("Watchers (%d)", st.Boards), accent: accent, lines: lines}}, w)
}

// linkTiles: the four numbers that matter, totalled over the watchers.
func linkTiles(snaps []LinkSnapshot, w int) string {
	rules, matching, queued, sent := 0, 0, 0, 0
	for _, s := range snaps {
		rules += len(s.Rules)
		queued += s.Queued
		sent += s.Sent
		for _, r := range s.Rules {
			matching += r.Matches
		}
	}
	tile := func(title string, n int, unit string, c lipgloss.Color) footBox {
		if n == 0 {
			c = lvGrey
		}
		return footBox{title: title, accent: lvGrey, lines: []string{bold.Foreground(c).Render(fmt.Sprint(n)) + dim.Render(" "+unit)}}
	}
	return boxRows([]footBox{
		tile("Rules", rules, "active", lvBlue),
		tile("Matching", matching, "cards", lvWhite),
		tile("Queued", queued, "to send", lvYellow),
		tile("Sent", sent, "total", lvGreen),
	}, w)
}

// linkRules: an aligned table of every watcher's rules with highlighted
// filters, with a board column when more than one watcher runs.
func (m model) linkRules(snaps []LinkSnapshot, w int) string {
	inner := w - 4
	type row struct {
		board string
		r     RuleView
	}
	var rows []row
	active := 0
	for _, s := range snaps {
		if s.Active {
			active++
		}
		for _, r := range s.Rules {
			rows = append(rows, row{firstNonEmpty(s.Title, s.Board), r})
		}
	}
	title := fmt.Sprintf("Rules (%d)", len(rows))
	if len(rows) == 0 {
		return boxRows([]footBox{{title: title, accent: lvBlue, lines: []string{
			dim.Render("No rules yet. An agent adds one with ") + ftKey.Render("cardhub notify add '<filter>'"),
		}}}, w)
	}
	const numW = 9 // "MATCHING" / "REPORTED" columns
	boardW := 0
	if active > 1 {
		boardW = min(20, inner/4)
	}
	filterW := max(10, inner-4-2*(numW+2)-boardW-2*boolInt(boardW > 0))
	head := fmt.Sprintf("%-3s ", "#")
	if boardW > 0 {
		head += fmt.Sprintf("%-*s  ", boardW, "BOARD")
	}
	head += fmt.Sprintf("%-*s  %*s  %*s", filterW, "FILTER", numW, "MATCHING", numW, "REPORTED")
	lines := []string{dim.Render(head)}
	for _, x := range rows {
		line := ftKey.Render(fmt.Sprintf("%-3d", x.r.ID)) + " "
		if boardW > 0 {
			b := trunc(x.board, boardW)
			line += fg(lvWhite).Render(b) + strings.Repeat(" ", max(0, boardW-lipgloss.Width(b))) + "  "
		}
		f := highlightFilter(trunc(x.r.Filter, filterW))
		line += f + strings.Repeat(" ", max(0, filterW-lipgloss.Width(f))) + "  " +
			bold.Foreground(lvWhite).Render(fmt.Sprintf("%*d", numW, x.r.Matches)) + "  " +
			fg(lvGreen).Render(fmt.Sprintf("%*d", numW, x.r.Reported))
		lines = append(lines, line)
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
		dim.Render("   Quitting CardHub stops the watchers, and a waiting agent is told (exit code 3)."),
		"",
		ftLabel.Render("Or paste this sample prompt into your agent ") + dim.Render("(") + ftKey.Render("C") + dim.Render(" copies it):"),
	}
	for _, l := range wrapWords(samplePrompt(), max(10, inner-4)) {
		lines = append(lines, fg(lvBlue).Render("▎ ")+fg(lipgloss.Color("#c9d1d9")).Render(l))
	}
	lines = append(lines, dim.Render(`With several boards watched, add --board "<project name>" to both commands.`))
	if m.copied != "" {
		lines = append(lines, fg(lvGreen).Render("✔ "+m.copied))
	}
	return boxRows([]footBox{{title: "Connect an agent", accent: lvGrey, lines: lines}}, w)
}

// linkActivity: one timeline for all watchers, newest first, tagged with the
// project when there's more than one, filling the space it's given.
func (m model) linkActivity(snaps []LinkSnapshot, w, h int) string {
	inner := w - 4
	rows := max(1, h-2)
	type entry struct {
		l     LinkLog
		board string
	}
	var all []entry
	for _, s := range snaps {
		for _, l := range s.Log {
			all = append(all, entry{l, firstNonEmpty(s.Title, s.Board)})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].l.At.After(all[j].l.At) })
	tagged := len(snaps) > 1
	icons := map[string]struct {
		icon string
		c    lipgloss.Color
	}{
		"rule": {"✚", lvBlue}, "queued": {"◆", lvYellow}, "sent": {"➜", lvGreen},
		"agent": {"◉", lvPurple}, "stop": {"■", lvRed},
	}
	var lines []string
	if len(all) == 0 {
		lines = append(lines, dim.Render("Nothing yet."))
	}
	for i, e := range all {
		if i == rows {
			break
		}
		ic := icons[e.l.Kind]
		prefix := dim.Render(e.l.At.Format("15:04:05")) + "  " + fg(ic.c).Render(ic.icon) + "  "
		if tagged {
			prefix += fg(lvBlue).Render(trunc(e.board, 14)) + " "
		}
		room := inner - lipgloss.Width(prefix)
		var text string
		if c := e.l.Card; c != nil {
			num := bold.Foreground(lvWhite).Render(fmt.Sprintf("#%d", c.Number))
			tag := fg(ghColor(m.columnColor(c.Status))).Render("[" + c.Status + "]")
			rule := dim.Render(fmt.Sprintf(" · rule %d", c.Rule))
			titleRoom := room - lipgloss.Width(num) - lipgloss.Width(tag) - lipgloss.Width(rule) - 2
			text = num + " " + tag + " " + ftLabel.Render(trunc(c.Title, max(4, titleRoom))) + rule
		} else {
			text = ftLabel.Render(trunc(e.l.Text, room))
		}
		lines = append(lines, prefix+text)
	}
	return boxRows([]footBox{{title: "Activity", accent: lvPurple, lines: lines}}, w)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
// board (the commands use the only watched one) and filters on GitHub's
// default "bug" label, so it works on any project as is.
func samplePrompt() string {
	return "Read `cardhub guide`. Run `cardhub notify add 'is:open label:bug' --no-open`, then run `cardhub events --wait --json` in the background. " +
		"When it returns, handle each reported card, oldest first, then run `events --wait` again. " +
		"If it exits with code 3 or 4, CardHub was closed: stop and say so."
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
