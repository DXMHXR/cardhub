package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// The Stats screen counts the cards that moved between columns while this
// cardhub runs, on every board it shows: both boards of a split, and the
// projects opened earlier in the session. It fetches nothing of its own; the
// moves come from the boards' regular syncs (Diff).

// movedCard is one card changing column.
type movedCard struct {
	at       time.Time
	project  string // the project's title
	from, to string
	toColor  string // the destination column's GitHub color
	number   int    // 0 for a move inferred from column totals (card not loaded)
	title    string
}

// moveSection totals one project, grouped by one field.
type moveSection struct {
	key, title string
	total      int
	routes     map[[2]string]int // from, to → moves
	colors     map[string]string // column → GitHub color
}

type moveLog struct {
	since    time.Time
	sections []*moveSection // in the order their first move came
	recent   []movedCard    // newest last, at most maxRecent
	lastTo   map[string]string
}

const maxRecent = 200

func newMoveLog() *moveLog {
	return &moveLog{since: time.Now(), lastTo: map[string]string{}}
}

// recordMoves adds the moves among evs, the events of the focused pane's
// latest sync (b). A move the split's other board, on the same project and
// grouping, already counted is skipped.
func (m *model) recordMoves(evs []Event, b *Board) {
	l := m.moveLog
	if l == nil {
		return
	}
	key := fmt.Sprintf("%s/%d/%s", m.owner, m.number, m.groupField)
	twin := m.split && m.other.owner == m.owner && m.other.number == m.number && m.other.groupField == m.groupField
	items := map[string]Item{}
	for _, it := range b.Items {
		items[it.ID] = it
	}
	colors := map[string]string{}
	for _, c := range m.columns {
		colors[c.name] = c.color
	}
	for _, e := range evs {
		if e.Kind != EvMoved {
			continue
		}
		if e.ItemID == "" && twin { // inferred from totals: the twin may infer it too
			continue
		}
		if e.ItemID != "" {
			if l.lastTo[key+"|"+e.ItemID] == e.To {
				continue
			}
			l.lastTo[key+"|"+e.ItemID] = e.To
		}
		s := l.section(key, m.projectTitle())
		s.total++
		s.routes[[2]string{e.From, e.To}]++
		for _, name := range []string{e.From, e.To} {
			if c, ok := colors[name]; ok {
				s.colors[name] = c
			}
		}
		mc := movedCard{at: e.At, project: s.title, from: e.From, to: e.To, toColor: colors[e.To]}
		if it, ok := items[e.ItemID]; ok {
			mc.number, mc.title = it.Number, it.Title
		}
		l.recent = append(l.recent, mc)
	}
	if len(l.recent) > maxRecent {
		l.recent = l.recent[len(l.recent)-maxRecent:]
	}
}

func (l *moveLog) section(key, title string) *moveSection {
	for _, s := range l.sections {
		if s.key == key {
			return s
		}
	}
	s := &moveSection{key: key, title: title, routes: map[[2]string]int{}, colors: map[string]string{}}
	l.sections = append(l.sections, s)
	return s
}

// projectTitle names the focused pane's project, with its grouping when that
// isn't Status.
func (m model) projectTitle() string {
	t := fmt.Sprintf("%s/%d", m.owner, m.number)
	if m.meta != nil && m.meta.Title != "" {
		t = m.meta.Title
	}
	if m.groupField != "" && !strings.EqualFold(m.groupField, "Status") {
		t += " · by " + m.groupField
	}
	return t
}

func (m model) statsView(h int) string {
	l := m.moveLog
	var b strings.Builder
	n := 0
	for _, s := range l.sections {
		n += s.total
	}
	head := " · since " + l.since.Format("15:04")
	if n > 0 {
		head += fmt.Sprintf(" · %s in %s", plural(n, "move"), plural(len(l.sections), "project"))
	}
	b.WriteString(" " + bold.Render("Card moves this session") + dim.Render(head) + "\n\n")
	if n == 0 {
		b.WriteString(dim.Render(" No card has moved yet. Every board you open in this session counts here, both boards of a split too.") + "\n")
		return b.String()
	}

	type route struct {
		from, to string
		n        int
	}
	routeW, maxN := 0, 1
	for _, s := range l.sections {
		for r, c := range s.routes {
			routeW = max(routeW, runewidth.StringWidth(r[0])+runewidth.StringWidth(r[1])+3)
			maxN = max(maxN, c)
		}
	}
	routeW = min(routeW, 48)
	barW := clamp(m.width-routeW-16, 4, 30)
	lines := 2
	for _, s := range l.sections {
		var rs []route
		for r, c := range s.routes {
			rs = append(rs, route{r[0], r[1], c})
		}
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].n != rs[j].n {
				return rs[i].n > rs[j].n
			}
			return rs[i].from+rs[i].to < rs[j].from+rs[j].to
		})
		head := " " + bold.Render(trunc(s.title, routeW+barW-6))
		b.WriteString(head + strings.Repeat(" ", max(1, routeW+barW+8-lipgloss.Width(head))) + dim.Render(plural(s.total, "move")) + "\n")
		for _, r := range rs {
			name := trunc(r.from+" → "+r.to, routeW)
			fill := max(1, r.n*barW/maxN)
			bar := lipgloss.NewStyle().Foreground(ghColor(s.colors[r.to])).Render(strings.Repeat("█", fill)) +
				dim.Render(strings.Repeat("░", barW-fill))
			b.WriteString(fmt.Sprintf("   %s %s %4d\n", runewidth.FillRight(name, routeW), bar, r.n))
		}
		b.WriteString("\n")
		lines += len(rs) + 2
	}

	// the newest moves in the room that's left
	room := h - lines - 1
	if room < 1 {
		return b.String()
	}
	b.WriteString(" " + bold.Render("Recent") + "\n")
	projW := 0
	for _, s := range l.sections {
		projW = max(projW, runewidth.StringWidth(s.title))
	}
	projW = min(projW, 20)
	cardW := clamp(m.width-projW-routeW-14, 12, 50)
	for i := len(l.recent) - 1; i >= 0 && room > 0; i, room = i-1, room-1 {
		mc := l.recent[i]
		card := dim.Render(trunc("(a card past the loaded ones)", cardW))
		if mc.number > 0 || mc.title != "" {
			card = trunc(fmt.Sprintf("#%d %s", mc.number, mc.title), cardW)
		}
		to := lipgloss.NewStyle().Foreground(ghColor(mc.toColor)).Render(mc.to)
		b.WriteString(fmt.Sprintf(" %s  %s  %s  %s%s\n",
			dim.Render(mc.at.Format("15:04")),
			runewidth.FillRight(trunc(mc.project, projW), projW),
			card+strings.Repeat(" ", max(0, cardW-lipgloss.Width(card))),
			dim.Render(mc.from+" → "), to))
	}
	return b.String()
}
