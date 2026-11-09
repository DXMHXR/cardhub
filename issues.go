package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The Issues screen of Herdr's panel (gui.go): the open or closed issues of
// the repo the Pull requests screen shows, most recently updated first.
// Clicking one opens it in the detail view, its description and comments.

// Issue is one issue as the list shows it.
type Issue struct {
	ID, URL   string
	Number    int
	Title     string
	State     string // OPEN, CLOSED
	Reason    string // why it closed: COMPLETED, NOT_PLANNED, DUPLICATE
	Author    string
	Labels    []string
	Assignees []string
	Milestone string
	Comments  int
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  time.Time
}

// RepoIssues is one page of a repo's issues and the totals.
type RepoIssues struct {
	Repo         string
	Issues       []Issue
	Open, Closed int
	RateLimit    RateLimit
	FetchedAt    time.Time
}

// issuePage is how many issues the screen loads per state.
const issuePage = 30

const issuesQuery = `
query($owner: String!, $name: String!, $states: [IssueState!], $n: Int!) {
  rateLimit { cost remaining limit resetAt }
  repository(owner: $owner, name: $name) {
    nameWithOwner
    open: issues(states: [OPEN], first: 0) { totalCount }
    closed: issues(states: [CLOSED], first: 0) { totalCount }
    issues(states: $states, first: $n, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        id url number title state stateReason
        author { login }
        createdAt updatedAt closedAt
        milestone { title }
        comments { totalCount }
        labels(first: 5) { nodes { name } }
        assignees(first: 5) { nodes { login } }
      }
    }
  }
}`

// FetchIssues loads a repo's latest open (or closed) issues.
func (c *Client) FetchIssues(ctx context.Context, repo string, closed bool) (*RepoIssues, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%q isn't a repo: expected owner/name", repo)
	}
	states := []string{"OPEN"}
	if closed {
		states = []string{"CLOSED"}
	}
	type total struct {
		TotalCount int `json:"totalCount"`
	}
	var r struct {
		RateLimit rateLimitResp `json:"rateLimit"`
		Repo      *struct {
			NameWithOwner string `json:"nameWithOwner"`
			Open          total  `json:"open"`
			Closed        total  `json:"closed"`
			Issues        struct {
				Nodes []struct {
					ID        string       `json:"id"`
					URL       string       `json:"url"`
					Number    int          `json:"number"`
					Title     string       `json:"title"`
					State     string       `json:"state"`
					Reason    string       `json:"stateReason"`
					Author    *detailLogin `json:"author"`
					CreatedAt time.Time    `json:"createdAt"`
					UpdatedAt time.Time    `json:"updatedAt"`
					ClosedAt  time.Time    `json:"closedAt"`
					Milestone *struct {
						Title string `json:"title"`
					} `json:"milestone"`
					Comments  total  `json:"comments"`
					Labels    names  `json:"labels"`
					Assignees logins `json:"assignees"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "states": states, "n": issuePage}
	if err := c.graphql(ctx, issuesQuery, vars, &r); err != nil {
		if strings.Contains(err.Error(), "Could not resolve to a Repository") {
			return nil, fmt.Errorf("no repo %s, or your token can't read it", repo)
		}
		return nil, err
	}
	if r.Repo == nil {
		return nil, fmt.Errorf("no repo %s, or your token can't read it", repo)
	}
	out := &RepoIssues{Repo: r.Repo.NameWithOwner, Open: r.Repo.Open.TotalCount, Closed: r.Repo.Closed.TotalCount,
		RateLimit: RateLimit{r.RateLimit.Remaining, r.RateLimit.Limit, r.RateLimit.ResetAt}, FetchedAt: time.Now()}
	for _, n := range r.Repo.Issues.Nodes {
		is := Issue{ID: n.ID, URL: n.URL, Number: n.Number, Title: n.Title, State: n.State, Reason: n.Reason,
			Author: "ghost", Comments: n.Comments.TotalCount, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, ClosedAt: n.ClosedAt}
		if n.Author != nil && n.Author.Login != "" {
			is.Author = n.Author.Login
		}
		if n.Milestone != nil {
			is.Milestone = n.Milestone.Title
		}
		for _, l := range n.Labels.Nodes {
			is.Labels = append(is.Labels, l.Name)
		}
		for _, a := range n.Assignees.Nodes {
			is.Assignees = append(is.Assignees, a.Login)
		}
		out.Issues = append(out.Issues, is)
	}
	return out, nil
}

// issueScreen is the Issues screen's state; its repo is the Pull requests screen's.
type issueScreen struct {
	closed  bool
	data    *RepoIssues
	shown   bool // data holds the closed list
	loading bool
	err     error
	seq     int
}

type issuesMsg struct {
	seq  int
	data *RepoIssues
	err  error
}

func (m *model) fetchIssues() tea.Cmd {
	if m.prs.repo == "" || m.gui == nil {
		return nil
	}
	is := &m.gui.issues
	is.loading = true
	is.seq++
	seq, c, repo, closed := is.seq, m.client, m.prs.repo, is.closed
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := c.FetchIssues(ctx, repo, closed)
		return issuesMsg{seq: seq, data: d, err: err}
	}
}

func (m *model) gotIssues(msg issuesMsg) {
	is := &m.gui.issues
	if msg.seq != is.seq {
		return
	}
	is.loading, is.err = false, msg.err
	if msg.err != nil {
		return
	}
	is.data, is.shown = msg.data, is.closed
	if m.board != nil {
		m.board.RateLimit = msg.data.RateLimit
	}
}

// issuesDue reports whether the issue list is missing, for another repo, or stale.
func (m model) issuesDue() bool {
	is := m.gui.issues
	if m.prs.repo == "" || is.loading {
		return false
	}
	return is.data == nil || !strings.EqualFold(is.data.Repo, m.prs.repo) || is.shown != is.closed ||
		time.Since(is.data.FetchedAt) > m.effectiveInterval()
}

// issueItem turns an issue into the Item the detail view shows.
func issueItem(is Issue, repo string) Item {
	it := Item{ID: is.ID, Kind: "ISSUE", Title: is.Title, Number: is.Number, Repo: repo, URL: is.URL,
		State: is.State, Assignees: is.Assignees, Labels: is.Labels, UpdatedAt: is.UpdatedAt, Milestone: is.Milestone,
		Values: map[string]string{}}
	if is.State == "CLOSED" && !is.ClosedAt.IsZero() {
		why := map[string]string{"COMPLETED": "completed", "NOT_PLANNED": "not planned", "DUPLICATE": "duplicate"}[is.Reason]
		it.Values["Closed"] = is.ClosedAt.Local().Format("2 Jan 2006 15:04") + pick(why != "", " · "+why, "")
	}
	return it
}
