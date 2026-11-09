package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The Pull requests screen (2): one repo's pull requests, open or closed
// (closed includes merged), most recently updated first. P switches the repo,
// ←→ switches open/closed, Enter shows a pull request in the detail popup and
// M merges it, after picking how.

var (
	prOpenColor   = lipgloss.Color("#3fb950")
	prMergedColor = lipgloss.Color("#a371f7")
	prClosedColor = lipgloss.Color("#f85149")
	prDraftColor  = lipgloss.Color("#8b949e")
	prAccent      = lipgloss.Color("#a371f7")
)

type prScreen struct {
	repo     string
	closed   bool // showing closed and merged ones instead of open ones
	data     *RepoPRs
	shown    bool // data holds the closed list (else the open one)
	loading  bool
	err      error
	seq      int
	row, off int

	repos        []string // the viewer's repos, once listed
	reposLoading bool
	reposErr     error
	projRepos    map[string][]string // per project (owner/number): the repos it works on, recommended first

	merge   mergeDialog
	flash   string // "Merged #12", shown for a while
	flashAt time.Time
}

type mergeDialog struct {
	open    bool
	pr      PR
	methods []MergeMethod
	cursor  int
	merging bool
	err     error
}

type prsMsg struct {
	seq  int
	data *RepoPRs
	err  error
}

type reposMsg struct {
	list []string
	err  error
}

// projReposMsg carries the repos a project works on (the repo menu's recommendations).
type projReposMsg struct {
	project string
	list    []string
	err     error
}

type mergedMsg struct {
	pr     PR
	method MergeMethod
	err    error
}

// fetchPRs loads the screen's repo and state; it supersedes a fetch in flight.
func (m *model) fetchPRs() tea.Cmd {
	if m.prs.repo == "" {
		return nil
	}
	m.prs.loading = true
	m.prs.seq++
	seq, c, repo, closed := m.prs.seq, m.client, m.prs.repo, m.prs.closed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := c.FetchPRs(ctx, repo, closed)
		return prsMsg{seq: seq, data: d, err: err}
	}
}

func (m *model) gotPRs(msg prsMsg) {
	if msg.seq != m.prs.seq {
		return
	}
	m.prs.loading, m.prs.err = false, msg.err
	if msg.err != nil {
		return
	}
	// keep the cursor on the same pull request when it's still listed
	var keep int
	if p, ok := m.selectedPR(); ok {
		keep = p.Number
	}
	m.prs.data, m.prs.shown = msg.data, m.prs.closed
	if m.board != nil {
		m.board.RateLimit = msg.data.RateLimit
	}
	m.prs.row = 0
	for i, p := range msg.data.PRs {
		if p.Number == keep {
			m.prs.row = i
		}
	}
	m.clampPRs()
	if m.detail.open && m.detail.pr != nil { // a popup on one of them shows the fresh state
		for _, p := range msg.data.PRs {
			if p.ID == m.detail.pr.ID {
				p := p
				m.detail.pr, m.detail.item = &p, prItem(p, msg.data.Repo)
			}
		}
	}
}

func listRepos(c *Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := c.ListRepos(ctx)
		return reposMsg{list, err}
	}
}

func (m *model) gotRepos(msg reposMsg) {
	m.prs.reposLoading, m.prs.reposErr = false, msg.err
	if msg.err == nil {
		m.prs.repos = msg.list
		if m.picker.open && m.picker.kind == pickRepo {
			m.focusCurrent()
		}
	}
	if m.picker.open && m.picker.kind == pickRepo {
		m.picker.loading, m.picker.err = false, msg.err
	}
}

// openRepoPicker opens the repo menu, listing the viewer's repos the first
// time and the board's project's repos (the recommendations) once per project.
func (m *model) openRepoPicker() tea.Cmd {
	m.picker.open, m.picker.kind, m.picker.query, m.picker.split = true, pickRepo, "", false
	m.picker.err = nil
	m.focusCurrent()
	cmd := m.loadRepoLists()
	m.picker.loading = m.prs.reposLoading
	return cmd
}

// loadRepoLists fetches what the repo menu lists: the project's repos once per
// project and the viewer's repos once.
func (m *model) loadRepoLists() tea.Cmd {
	var cmds []tea.Cmd
	if key := m.projectKey(); key != "" {
		if _, ok := m.prs.projRepos[key]; !ok {
			if m.prs.projRepos == nil {
				m.prs.projRepos = map[string][]string{}
			}
			m.prs.projRepos[key] = nil // asked; filled in when the answer comes
			c, owner, number := m.client, m.owner, m.number
			cmds = append(cmds, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				list, err := c.ProjectRepos(ctx, owner, number)
				return projReposMsg{project: key, list: list, err: err}
			})
		}
	}
	if m.prs.repos == nil && !m.prs.reposLoading {
		m.prs.reposLoading = true
		cmds = append(cmds, listRepos(m.client))
	}
	return tea.Batch(cmds...)
}

// projectKey names the board's project ("owner/number"), "" with none open.
func (m model) projectKey() string {
	if m.owner == "" {
		return ""
	}
	return fmt.Sprintf("%s/%d", m.owner, m.number)
}

func (m *model) gotProjRepos(msg projReposMsg) {
	if msg.err != nil { // the menu still lists the board's loaded cards' repos
		delete(m.prs.projRepos, msg.project)
		return
	}
	m.prs.projRepos[msg.project] = msg.list
	if m.picker.open && m.picker.kind == pickRepo && m.picker.query == "" {
		m.focusCurrent()
	}
}

// recommendedRepos are the repos the board's project works on: first the
// ones of the cards on screen (most cards first), then the rest of the
// project's (from its first 100 cards, then the repos linked to it).
func (m model) recommendedRepos() []string {
	out := boardRepos(m.board)
	for _, r := range m.prs.projRepos[m.projectKey()] {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// repoRows is the repo menu: the repos the board's project works on first
// (★, recommended), then the rest of the viewer's.
func (m model) repoRows() []pickRow {
	var rows []pickRow
	seen := map[string]bool{}
	add := func(r, meta string) {
		if seen[strings.ToLower(r)] {
			return
		}
		seen[strings.ToLower(r)] = true
		rows = append(rows, pickRow{meta: meta, title: r, current: strings.EqualFold(r, m.prs.repo), repo: r})
	}
	for _, r := range m.recommendedRepos() {
		add(r, "★ project")
	}
	if m.prs.repo != "" {
		add(m.prs.repo, "")
	}
	for _, r := range m.prs.repos {
		add(r, "")
	}
	// a repo typed in full comes first, and opens even when it isn't listed
	if q := strings.TrimSpace(m.picker.query); strings.Count(q, "/") == 1 && !strings.ContainsAny(q, " ") {
		exact := pickRow{meta: "open", title: q, repo: q}
		for i, r := range rows {
			if strings.EqualFold(r.repo, q) {
				exact = r
				rows = append(rows[:i:i], rows[i+1:]...)
				break
			}
		}
		rows = append([]pickRow{exact}, rows...)
	}
	return rows
}

// leaveRepo empties the screen when the board moves to another project, which
// has its own repo; the repo lists stay cached.
func (m *model) leaveRepo() {
	m.prs.seq++ // drops a fetch still on its way
	m.prs.repo, m.prs.data, m.prs.err, m.prs.loading = "", nil, nil, false
	m.prs.row, m.prs.off, m.prs.closed, m.prs.shown = 0, 0, false, false
}

// switchRepo shows another repo's pull requests.
func (m *model) switchRepo(repo string) tea.Cmd {
	m.view = viewPRs
	if strings.EqualFold(repo, m.prs.repo) && m.prs.data != nil {
		return nil
	}
	m.prs.repo, m.prs.data, m.prs.err, m.prs.row, m.prs.off = repo, nil, nil, 0, 0
	if key := m.projectKey(); key != "" {
		if m.saved.PRRepos == nil {
			m.saved.PRRepos = map[string]string{}
		}
		m.saved.PRRepos[key] = repo
		saveLast(m.saved)
	}
	return m.fetchPRs()
}

// enterPRs is called when the screen comes up: it opens the repo last picked
// for this project, asks for one the first time, and refreshes a list that's
// gone stale meanwhile.
func (m *model) enterPRs() tea.Cmd {
	if m.prs.repo == "" {
		if r := m.saved.PRRepos[m.projectKey()]; r != "" {
			m.prs.repo = r
		} else {
			return m.openRepoPicker()
		}
	}
	if !m.prs.loading && (m.prs.data == nil || time.Since(m.prs.data.FetchedAt) > m.interval) {
		return m.fetchPRs()
	}
	return nil
}

// duePRs refreshes the screen on the board's cadence while it's on screen.
func (m *model) duePRs() tea.Cmd {
	if m.view != viewPRs || m.paused || m.prs.loading || m.prs.data == nil {
		return nil
	}
	if time.Since(m.prs.data.FetchedAt) > m.effectiveInterval() {
		return m.fetchPRs()
	}
	return nil
}

func (m model) selectedPR() (PR, bool) {
	if m.prs.data != nil && m.prs.shown == m.prs.closed && m.prs.row < len(m.prs.data.PRs) {
		return m.prs.data.PRs[m.prs.row], true
	}
	return PR{}, false
}

// prRows is how many pull requests fit on screen.
func (m model) prRows() int {
	return max(1, m.boardH()-5)
}

func (m *model) clampPRs() {
	n := 0
	if m.prs.data != nil {
		n = len(m.prs.data.PRs)
	}
	vis := m.prRows()
	m.prs.row = clamp(m.prs.row, 0, max(0, n-1))
	if m.prs.row < m.prs.off {
		m.prs.off = m.prs.row
	}
	if m.prs.row >= m.prs.off+vis {
		m.prs.off = m.prs.row - vis + 1
	}
	m.prs.off = clamp(m.prs.off, 0, max(0, n-vis))
}

// prKey handles the keys the screen takes over; ok is false for the rest.
func (m *model) prKey(key string) (cmd tea.Cmd, ok bool) {
	switch key {
	case "up", "k":
		m.prs.row--
	case "down", "j":
		m.prs.row++
	case "pgup":
		m.prs.row -= m.prRows()
	case "pgdown":
		m.prs.row += m.prRows()
	case "home", "g":
		m.prs.row = 0
	case "end", "G":
		m.prs.row = 1 << 30
	case "left", "h", "right", "l", "s", "S":
		closed := key == "right" || key == "l"
		if key == "s" || key == "S" {
			closed = !m.prs.closed
		}
		if closed != m.prs.closed {
			m.prs.closed, m.prs.row, m.prs.off = closed, 0, 0 // the counts stay up while it loads
			return m.fetchPRs(), true
		}
		return nil, true
	case "p", "P":
		return m.openRepoPicker(), true
	case "r", "R":
		return m.fetchPRs(), true
	case "enter":
		if p, ok := m.selectedPR(); ok {
			return m.openPRDetail(p), true
		}
		return nil, true
	case "o":
		if p, ok := m.selectedPR(); ok {
			openURL(p.URL)
		}
		return nil, true
	case "O":
		if m.prs.repo != "" {
			openURL("https://github.com/" + m.prs.repo + "/pulls")
		}
		return nil, true
	case "m", "M":
		if p, ok := m.selectedPR(); ok {
			m.openMerge(p)
		}
		return nil, true
	case "esc", "v", "V": // the board's search and views don't apply here
		return nil, true
	default:
		return nil, false
	}
	m.clampPRs()
	return nil, true
}

// prItem turns a pull request into the Item the detail popup's header shows.
func prItem(p PR, repo string) Item {
	state := p.State
	if p.Draft && state == "OPEN" {
		state = "DRAFT"
	}
	return Item{ID: p.ID, Kind: "PULL_REQUEST", Title: p.Title, Number: p.Number, Repo: repo, URL: p.URL,
		State: state, Assignees: p.Assignees, Labels: p.Labels, UpdatedAt: p.UpdatedAt}
}

// openPRDetail opens the detail popup on a pull request.
func (m *model) openPRDetail(p PR) tea.Cmd {
	repo := m.prs.repo
	if m.prs.data != nil {
		repo = m.prs.data.Repo
	}
	return m.openDetailOn(prItem(p, repo), prColor(p), &p)
}

func prColor(p PR) lipgloss.Color {
	switch {
	case p.State == "MERGED":
		return prMergedColor
	case p.State == "CLOSED":
		return prClosedColor
	case p.Draft:
		return prDraftColor
	}
	return prOpenColor
}

func prIcon(p PR) string {
	icon := map[string]string{"OPEN": "●", "MERGED": "◆", "CLOSED": "✕"}[p.State]
	if p.Draft && p.State == "OPEN" {
		icon = "◌"
	}
	return lipgloss.NewStyle().Foreground(prColor(p)).Render(icon)
}

// checksMark is a pull request's CI status as one coloured mark.
func checksMark(state string) string {
	switch state {
	case "SUCCESS":
		return fg(prOpenColor).Render("✓")
	case "FAILURE", "ERROR":
		return fg(prClosedColor).Render("✗")
	case "PENDING", "EXPECTED":
		return fg(lipgloss.Color("#d29922")).Render("◷")
	}
	return " "
}

func reviewTag(decision string) string {
	switch decision {
	case "APPROVED":
		return fg(prOpenColor).Render("approved")
	case "CHANGES_REQUESTED":
		return fg(prClosedColor).Render("changes requested")
	case "REVIEW_REQUIRED":
		return fg(lipgloss.Color("#d29922")).Render("review required")
	}
	return ""
}

// reviewShort is reviewTag for a list row.
func reviewShort(decision string) string {
	switch decision {
	case "APPROVED":
		return fg(prOpenColor).Render("approved")
	case "CHANGES_REQUESTED":
		return fg(prClosedColor).Render("changes req.")
	case "REVIEW_REQUIRED":
		return fg(lipgloss.Color("#d29922")).Render("review req.")
	}
	return ""
}

// whyNoMerge says why a pull request can't be merged from here, or "" when it can.
func (m model) whyNoMerge(p PR) string {
	switch {
	case p.State == "MERGED":
		return "#" + fmt.Sprint(p.Number) + " is already merged."
	case p.State == "CLOSED":
		return "#" + fmt.Sprint(p.Number) + " is closed."
	case p.Draft:
		return "#" + fmt.Sprint(p.Number) + " is a draft: mark it ready for review on GitHub first."
	case m.prs.data == nil:
		return "The repo isn't loaded yet."
	case !m.prs.data.CanMerge:
		return "You don't have write access to " + m.prs.data.Repo + "."
	case len(m.prs.data.Methods()) == 0:
		return m.prs.data.Repo + " allows no merge method."
	}
	return ""
}

// openMerge opens the merge dialog on a pull request.
func (m *model) openMerge(p PR) {
	m.prs.merge = mergeDialog{open: true, pr: p}
	if m.prs.data != nil {
		m.prs.merge.methods = m.prs.data.Methods()
	}
}

func (m model) mergeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.prs.merge
	if d.merging { // wait for GitHub's answer
		if k.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n", "N", "q":
		d.open = false
	case "up", "k", "left", "h", "shift+tab":
		d.cursor = max(0, d.cursor-1)
	case "down", "j", "right", "l", "tab":
		d.cursor = min(max(0, len(d.methods)-1), d.cursor+1)
	case "1", "2", "3":
		if i := int(k.String()[0] - '1'); i < len(d.methods) {
			d.cursor = i
		}
	case "enter", "y", "Y":
		if m.whyNoMerge(d.pr) != "" || d.cursor >= len(d.methods) {
			d.open = false
			return m, nil
		}
		d.merging, d.err = true, nil
		c, pr, method := m.client, d.pr, d.methods[d.cursor]
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			return mergedMsg{pr: pr, method: method, err: c.MergePR(ctx, pr.ID, method)}
		}
	}
	return m, nil
}

func (m *model) gotMerged(msg mergedMsg) tea.Cmd {
	d := &m.prs.merge
	d.merging = false
	if msg.err != nil {
		d.err = msg.err
		return nil
	}
	d.open = false
	m.prs.flash, m.prs.flashAt = fmt.Sprintf("Merged #%d (%s)", msg.pr.Number, strings.ToLower(msg.method.Label())), time.Now()
	return m.fetchPRs() // shows it merged (it leaves the open list)
}

// mergeBox renders the merge dialog.
func (m model) mergeBox() string {
	d := m.prs.merge
	w := clamp(m.width-8, 30, 64)
	inner := w - 4
	p := d.pr
	lines := []string{
		bold.Foreground(prMergedColor).Render("Merge pull request #" + fmt.Sprint(p.Number) + "?"),
		ftLabel.Render(trunc(p.Title, inner)),
		dim.Render(trunc(p.Head+" → "+p.Base, inner)),
		"",
	}
	if why := m.whyNoMerge(p); why != "" {
		lines = append(lines, errStyle.Width(inner).Render(why), "", ftKey.Render("Esc")+ftLabel.Render(" close"))
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(prClosedColor).
			Padding(0, 1).Width(w - 2).Render(strings.Join(lines, "\n"))
	}
	yellow := lipgloss.Color("#d29922")
	switch p.Mergeable {
	case "CONFLICTING":
		lines = append(lines, fg(prClosedColor).Render("! ")+ftLabel.Render("This branch has conflicts: GitHub will refuse to merge it."), "")
	case "UNKNOWN":
		lines = append(lines, fg(yellow).Render("! ")+ftLabel.Render("GitHub is still checking whether it merges cleanly."), "")
	}
	switch p.ReviewDecision {
	case "CHANGES_REQUESTED":
		lines = append(lines, fg(yellow).Render("! ")+ftLabel.Render("A reviewer requested changes."), "")
	case "REVIEW_REQUIRED":
		lines = append(lines, fg(yellow).Render("! ")+ftLabel.Render("It still needs an approving review; branch protection may block the merge."), "")
	}
	if p.Checks == "FAILURE" || p.Checks == "ERROR" {
		lines = append(lines, fg(yellow).Render("! ")+ftLabel.Render("Some checks are failing."), "")
	}
	for i, mm := range d.methods {
		mark, st := "  ○ ", ftLabel
		if i == d.cursor {
			mark, st = fg(prMergedColor).Render("  ● "), bold
		}
		lines = append(lines, mark+st.Render(fmt.Sprintf("%d  %s", i+1, mm.Label())))
	}
	lines = append(lines, "")
	switch {
	case d.merging:
		lines = append(lines, m.spin.View()+dim.Render(" merging…"))
	case d.err != nil:
		lines = append(lines, errStyle.Width(inner).Render("Merge failed: "+d.err.Error()), "")
		fallthrough
	default:
		lines = append(lines, ftKey.Render("↑↓")+ftLabel.Render(" method   ")+
			ftKey.Render("Enter")+ftLabel.Render(" merge   ")+ftKey.Render("Esc")+ftLabel.Render(" cancel"))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(prMergedColor).
		Padding(0, 1).Width(w - 2).Render(lipgloss.NewStyle().Width(inner).Render(strings.Join(lines, "\n")))
}

// prsView draws the screen in a w×h area.
func (m model) prsView(w, h int) string {
	if m.prs.repo == "" {
		return dim.Render("  No repo picked yet. ") + ftKey.Render("P") + dim.Render(" picks one.")
	}
	inner := w - 4

	// head: repo, Open / Closed tabs, refresh state
	tab := func(label string, n int, on bool, color lipgloss.Color) string {
		s := label
		if m.prs.data != nil {
			s += fmt.Sprintf(" %d", n)
		}
		if on {
			return fg(color).Render("● ") + tabOn.Render(s)
		}
		return dim.Render("○ ") + tabOff.Render(s)
	}
	var open, closed int
	if m.prs.data != nil {
		open, closed = m.prs.data.Open, m.prs.data.Closed
	}
	head := bold.Render(m.prs.repo) + "   " + tab("Open", open, !m.prs.closed, prOpenColor) + "  " +
		tab("Closed", closed, m.prs.closed, prMergedColor)
	var status string
	switch {
	case m.prs.loading:
		status = m.spin.View() + dim.Render(" loading…")
	case m.prs.flash != "" && time.Since(m.prs.flashAt) < 10*time.Second:
		status = fg(prMergedColor).Bold(true).Render("✓ " + m.prs.flash)
	case m.prs.data != nil:
		status = dim.Render("updated " + ago(m.prs.data.FetchedAt) + " ago")
	}
	head += strings.Repeat(" ", max(1, inner-lipgloss.Width(head)-lipgloss.Width(status))) + status

	var body []string
	vis := max(1, h-5)
	switch {
	case m.prs.err != nil && (m.prs.data == nil || m.prs.shown != m.prs.closed):
		body = append(body, errStyle.Width(inner).Render(m.prs.err.Error()))
	case m.prs.data == nil || m.prs.shown != m.prs.closed:
		body = append(body, m.spin.View()+dim.Render(" loading pull requests…"))
	case len(m.prs.data.PRs) == 0:
		body = append(body, dim.Render(pick(m.prs.closed, "No closed pull requests.", "No open pull requests.")))
	default:
		prs := m.prs.data.PRs
		numW := 0
		for _, p := range prs {
			numW = max(numW, len(fmt.Sprint(p.Number))+1)
		}
		for i := m.prs.off; i < len(prs) && i < m.prs.off+vis; i++ {
			body = append(body, m.prLine(prs[i], i == m.prs.row, inner, numW))
		}
	}
	for len(body) < vis {
		body = append(body, "")
	}

	var foot string
	if m.prs.data != nil && m.prs.shown == m.prs.closed && len(m.prs.data.PRs) > 0 {
		n := len(m.prs.data.PRs)
		total := pick(m.prs.closed, m.prs.data.Closed, m.prs.data.Open)
		foot = fmt.Sprintf("%d of %d", m.prs.row+1, n)
		if total > n {
			foot += fmt.Sprintf(" · latest %d of %d (O opens them all on GitHub)", n, total)
		}
		if m.prs.err != nil {
			foot += " · " + errStyle.Render(m.prs.err.Error())
		}
		foot = dim.Render(trunc(foot, inner))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, head, ftRule.Render(strings.Repeat("─", inner)), strings.Join(body, "\n"), foot)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#30363d")).
		Padding(0, 1).Width(w - 2).Render(content)
}

// prLine is one row: state, number, title, branch and author on the left;
// review, checks, size and age on the right.
func (m model) prLine(p PR, sel bool, w, numW int) string {
	bar, titleSt, metaSt := " ", lipgloss.NewStyle().Foreground(lipgloss.Color("#c9d1d9")), dim
	if sel {
		bar = lipgloss.NewStyle().Foreground(selBar).Bold(true).Render("▌")
		titleSt, metaSt = bold.Foreground(lipgloss.Color("#ffffff")), lipgloss.NewStyle().Foreground(selText)
	}
	when := p.UpdatedAt
	verb := "updated"
	switch p.State {
	case "MERGED":
		when, verb = p.MergedAt, "merged"
	case "CLOSED":
		when, verb = p.ClosedAt, "closed"
	}
	// fixed-width cells, so they line up from row to row
	cell := func(s string, w int) string { return strings.Repeat(" ", max(0, w-lipgloss.Width(s))) + s }
	review := ""
	if p.State == "OPEN" {
		review = reviewShort(p.ReviewDecision)
	}
	comments := ""
	if p.Comments > 0 {
		comments = metaSt.Render(fmt.Sprintf("💬%d", p.Comments))
	}
	diff := fg(prOpenColor).Render(fmt.Sprintf("+%d", p.Additions)) + " " + fg(prClosedColor).Render(fmt.Sprintf("−%d", p.Deletions))
	right := cell(review, 12) + "  " + cell(comments, 4) + "  " + cell(diff, 12) + "  " + checksMark(p.Checks) +
		"  " + metaSt.Render(fmt.Sprintf("%-15s", verb+" "+shortAgo(when)))
	num := metaSt.Render(fmt.Sprintf("%*s", numW, fmt.Sprintf("#%d", p.Number)))
	left := bar + prIcon(p) + " " + num + " "
	meta := "  " + "@" + p.Author
	if p.Draft && p.State == "OPEN" {
		meta = "  draft" + meta
	}
	room := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if room < 20 { // narrow: drop the right side
		right, room = "", w-lipgloss.Width(left)
	}
	title := trunc(p.Title, max(1, room))
	rest := trunc(meta, max(0, room-lipgloss.Width(title)))
	line := left + titleSt.Render(title) + metaSt.Render(rest)
	return line + strings.Repeat(" ", max(1, w-lipgloss.Width(line)-lipgloss.Width(right))) + right
}

// shortAgo is a compact age: 5m, 3h, 2d, 4w, 2y.
func shortAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", max(1, int(d.Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	}
	return fmt.Sprintf("%dy ago", int(d.Hours()/24/365))
}
