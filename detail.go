package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// The card detail popup (Enter on a card): everything the board knows about
// the card at once, then its description and latest comments, fetched from
// GitHub when it opens. It scrolls; O opens the card on GitHub, Esc closes it.

// CardDetail is what the popup loads on demand: the parts of a card the board
// doesn't fetch on every sync.
type CardDetail struct {
	Author    string
	CreatedAt time.Time
	Body      string
	Comments  []Comment
	Total     int // all comments, of which Comments are the latest
}

type Comment struct {
	Author    string
	CreatedAt time.Time
	Body      string
}

// detailComments is how many of the latest comments the popup shows.
const detailComments = 20

const detailQuery = `
query($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      body createdAt author { login }
      comments(last: 20) { totalCount nodes { body createdAt author { login } } }
    }
    ... on ProjectV2Item {
      content {
        ... on DraftIssue { body createdAt creator { login } }
        ... on Issue {
          body createdAt author { login }
          comments(last: 20) { totalCount nodes { body createdAt author { login } } }
        }
        ... on PullRequest {
          body createdAt author { login }
          comments(last: 20) { totalCount nodes { body createdAt author { login } } }
        }
      }
    }
  }
}`

type detailLogin struct {
	Login string `json:"login"`
}

// detailContent is an issue's, pull request's or draft's text and comments.
type detailContent struct {
	Body      string       `json:"body"`
	CreatedAt time.Time    `json:"createdAt"`
	Author    *detailLogin `json:"author"`
	Creator   *detailLogin `json:"creator"`
	Comments  struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Body      string       `json:"body"`
			CreatedAt time.Time    `json:"createdAt"`
			Author    *detailLogin `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
}

// FetchDetail loads the description and latest comments of a project item or
// a pull request, by node ID.
func (c *Client) FetchDetail(ctx context.Context, id string) (*CardDetail, error) {
	type login = detailLogin
	var r struct {
		Node *struct {
			detailContent                // a pull request's own
			Content       *detailContent `json:"content"` // a project item's
		} `json:"node"`
	}
	if err := c.graphql(ctx, detailQuery, map[string]any{"id": id}, &r); err != nil {
		return nil, err
	}
	if r.Node == nil {
		return nil, fmt.Errorf("this card's content isn't readable with your token")
	}
	ct := r.Node.Content
	if ct == nil {
		if strings.HasPrefix(id, "PVTI_") { // a project item whose content is hidden
			return nil, fmt.Errorf("this card's content isn't readable with your token")
		}
		ct = &r.Node.detailContent
	}
	name := func(l *login) string {
		if l == nil || l.Login == "" {
			return "ghost"
		}
		return l.Login
	}
	d := &CardDetail{Body: ct.Body, CreatedAt: ct.CreatedAt, Total: ct.Comments.TotalCount}
	if ct.Author != nil {
		d.Author = name(ct.Author)
	} else if ct.Creator != nil {
		d.Author = name(ct.Creator)
	}
	for _, n := range ct.Comments.Nodes {
		d.Comments = append(d.Comments, Comment{Author: name(n.Author), CreatedAt: n.CreatedAt, Body: n.Body})
	}
	return d, nil
}

// detail is the popup's state.
type detail struct {
	open    bool
	item    Item
	color   lipgloss.Color // the card's column colour
	loading bool
	data    *CardDetail
	err     error
	off     int // first line on screen
	seq     int // tags fetches, so a late reply for a closed popup is dropped
	pr      *PR // set when it shows a pull request from the Pull requests screen
}

type detailMsg struct {
	seq  int
	data *CardDetail
	err  error
}

// openDetail opens the popup on the selected card and starts loading it.
func (m *model) openDetail() tea.Cmd {
	it, ok := m.selected()
	if !ok {
		return nil
	}
	color := lipgloss.Color(ghColors["GRAY"])
	if !m.isTable() && m.col < len(m.columns) {
		color = ghColor(m.columns[m.col].color)
	}
	return m.openDetailOn(it, color, nil)
}

// openDetailOn opens the popup on it (pr set for a pull request) and starts loading it.
func (m *model) openDetailOn(it Item, color lipgloss.Color, pr *PR) tea.Cmd {
	seq := m.detail.seq + 1
	m.detail = detail{open: true, item: it, color: color, seq: seq, pr: pr}
	if it.Kind == "REDACTED" {
		return nil
	}
	m.detail.loading = true
	c, id := m.client, it.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := c.FetchDetail(ctx, id)
		return detailMsg{seq: seq, data: d, err: err}
	}
}

func (m *model) gotDetail(msg detailMsg) {
	if !m.detail.open || msg.seq != m.detail.seq {
		return
	}
	m.detail.loading, m.detail.data, m.detail.err = false, msg.data, msg.err
}

// detailSize is the popup's outer width and the number of body lines it shows.
func (m model) detailSize() (w, h int) {
	w = clamp(m.width-6, 30, 100)
	h = max(3, m.height-4-6) // border, header lines and hint
	return w, h
}

func (m model) detailKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	_, h := m.detailSize()
	maxOff := max(0, len(m.detailLines())-h)
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "Q", "enter", "backspace":
		m.detail.open = false
	case "up", "k":
		m.detail.off--
	case "down", "j":
		m.detail.off++
	case "pgup", "b", "ctrl+u":
		m.detail.off -= h - 1
	case "pgdown", " ", "space", "f", "ctrl+d":
		m.detail.off += h - 1
	case "home", "g":
		m.detail.off = 0
	case "end", "G":
		m.detail.off = maxOff
	case "o", "O":
		if m.detail.item.URL != "" {
			openURL(m.detail.item.URL)
		}
	case "m", "M":
		if m.detail.pr != nil {
			m.openMerge(*m.detail.pr)
		}
	}
	m.detail.off = clamp(m.detail.off, 0, maxOff)
	return m, nil
}

// detailLines is the popup's scrolling part, wrapped to its width.
func (m model) detailLines() []string {
	w, _ := m.detailSize()
	inner := w - 4
	it, d := m.detail.item, m.detail.data
	var out []string
	add := func(s ...string) { out = append(out, s...) }
	// labels line up in one column as wide as the longest (at most 20)
	keyW := 10
	for k := range it.Values {
		keyW = max(keyW, min(20, runewidth.StringWidth(k)))
	}
	row := func(k, v string) {
		if v == "" {
			return
		}
		key := ftLabel.Render(runewidth.FillRight(trunc(k, keyW), keyW))
		add(strings.Split(key+" "+lipgloss.NewStyle().Width(inner-keyW-1).Render(v), "\n")...)
	}
	section := func(title string) {
		add("", bold.Foreground(m.detail.color).Render(title)+" "+ftRule.Render(strings.Repeat("─", max(0, inner-lipgloss.Width(title)-1))))
	}

	if p := m.detail.pr; p != nil {
		row("Author", "@"+p.Author)
		row("Branch", p.Head+" → "+p.Base)
		row("Changes", fmt.Sprintf("+%d −%d in %s", p.Additions, p.Deletions, plural(p.Files, "file")))
		if p.State == "OPEN" {
			row("Review", firstNonEmpty(reviewTag(p.ReviewDecision), "not required"))
			row("Mergeable", map[string]string{"MERGEABLE": "yes", "CONFLICTING": "no: conflicts", "UNKNOWN": "checking…"}[p.Mergeable])
		}
		if len(p.Reviews) > 0 {
			row("Reviews", strings.Join(p.Reviews, ", "))
		}
		row("Checks", map[string]string{"SUCCESS": "✓ passing", "FAILURE": "✗ failing", "ERROR": "✗ error", "PENDING": "◷ running", "EXPECTED": "◷ expected"}[p.Checks])
		if p.State == "MERGED" {
			row("Merged", p.MergedAt.Local().Format("2 Jan 2006 15:04")+pick(p.MergedBy != "", " by @"+p.MergedBy, ""))
		}
		if p.State == "CLOSED" {
			row("Closed", p.ClosedAt.Local().Format("2 Jan 2006 15:04"))
		}
	}
	if len(it.Assignees) > 0 {
		row("Assignees", "@"+strings.Join(it.Assignees, ", @"))
	}
	if len(it.Labels) > 0 {
		row("Labels", strings.Join(it.Labels, ", "))
	}
	keys := make([]string, 0, len(it.Values))
	for k := range it.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "Title" && k != "Assignees" && k != "Labels" {
			row(k, it.Values[k])
		}
	}
	row("Milestone", it.Milestone)
	row("Parent", it.Parent)
	if it.SubTotal > 0 {
		row("Sub-issues", fmt.Sprintf("%d/%d done", it.SubDone, it.SubTotal))
	}
	if !it.UpdatedAt.IsZero() {
		row("Updated", it.UpdatedAt.Local().Format("2 Jan 2006 15:04"))
	}

	wrap := func(md string) []string {
		md = strings.TrimSpace(cleanMarkdown(md))
		if md == "" {
			return []string{dim.Render("(empty)")}
		}
		return strings.Split(lipgloss.NewStyle().Width(inner).Render(md), "\n")
	}
	section("Description")
	switch {
	case it.Kind == "REDACTED":
		add(dim.Render("You can't see this card's content."))
	case m.detail.loading:
		add(m.spin.View() + dim.Render(" loading…"))
	case m.detail.err != nil:
		add(strings.Split(errStyle.Width(inner).Render(m.detail.err.Error()), "\n")...)
	case d != nil:
		if d.Author != "" {
			add(dim.Render("@" + d.Author + " opened " + d.CreatedAt.Local().Format("2 Jan 2006 15:04")))
			add("")
		}
		add(wrap(d.Body)...)
		if d.Total > 0 {
			title := fmt.Sprintf("Comments (%d)", d.Total)
			if d.Total > len(d.Comments) {
				title = fmt.Sprintf("Comments (latest %d of %d)", len(d.Comments), d.Total)
			}
			section(title)
			for i, c := range d.Comments {
				if i > 0 {
					add("")
				}
				add(ftKey.Render("@"+c.Author) + dim.Render(" · "+c.CreatedAt.Local().Format("2 Jan 2006 15:04")))
				add(wrap(c.Body)...)
			}
		}
	}
	return out
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	blankRuns   = regexp.MustCompile(`\n{3,}`)
)

// cleanMarkdown tidies a GitHub body for the terminal: no HTML comments (issue
// templates are full of them), no \r, at most one blank line in a row, and
// headings in bold without their #s.
func cleanMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = htmlComment.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\t", "    ")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines { // "## Heading" → bold Heading
		if t := strings.TrimLeft(l, "#"); t != l && len(l)-len(t) <= 6 && strings.HasPrefix(t, " ") {
			lines[i] = bold.Render(strings.TrimSpace(t))
		}
	}
	return strings.Join(lines, "\n")
}

// detailBox renders the popup.
func (m model) detailBox() string {
	w, h := m.detailSize()
	inner := w - 4
	it := m.detail.item

	title := bold.Render(trunc(it.Title, inner))
	kind := map[string]string{"ISSUE": "Issue", "PULL_REQUEST": "Pull request", "DRAFT_ISSUE": "Draft"}[it.Kind]
	meta := []string{}
	if kind != "" {
		meta = append(meta, kind)
	}
	if it.Number != 0 {
		meta = append(meta, it.Ref())
	}
	if it.State != "" {
		meta = append(meta, strings.ToLower(it.State))
	}
	sub := lipgloss.NewStyle().Foreground(m.detail.color).Render("● ") + dim.Render(trunc(strings.Join(meta, " · "), inner-2))

	lines := m.detailLines()
	off := clamp(m.detail.off, 0, max(0, len(lines)-h))
	end := min(off+h, len(lines))
	view := append([]string{}, lines[off:end]...)
	for len(view) < h {
		view = append(view, "")
	}

	pos := ""
	if len(lines) > h {
		pos = fmt.Sprintf("%d–%d of %d", off+1, end, len(lines))
		if off > 0 {
			pos = "▲ " + pos
		}
		if end < len(lines) {
			pos += " ▼"
		}
	}
	hint := ftKey.Render("↑↓") + ftLabel.Render(" scroll  ") + ftKey.Render("PgUp/PgDn") + ftLabel.Render(" page  ")
	if m.detail.pr != nil && m.detail.pr.State == "OPEN" {
		hint += ftKey.Render("M") + ftLabel.Render(" merge  ")
	}
	if it.URL != "" {
		hint += ftKey.Render("O") + ftLabel.Render(" open on GitHub  ")
	}
	hint += ftKey.Render("Esc") + ftLabel.Render(" close")
	foot := hint + strings.Repeat(" ", max(1, inner-lipgloss.Width(hint)-lipgloss.Width(pos))) + dim.Render(pos)

	body := lipgloss.JoinVertical(lipgloss.Left,
		title, sub, ftRule.Render(strings.Repeat("─", inner)),
		strings.Join(view, "\n"),
		ftRule.Render(strings.Repeat("─", inner)), foot)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(m.detail.color).
		Padding(0, 1).Width(w - 2).Render(body)
}
