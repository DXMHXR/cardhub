package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Board is a point-in-time snapshot of a GitHub Projects (v2) board.
type Board struct {
	Title     string
	URL       string
	Fields    []SelectField // single-select fields, usable for grouping
	Items     []Item
	FetchedAt time.Time
	RateLimit RateLimit
	Cost      int // API points this snapshot cost (summed over its pages)

	// Windowed snapshots (FetchColumns) hold only the first few cards of each
	// column; Totals has every column's real size and Order the column order.
	// Full snapshots get Totals/Order filled in from their items by the model.
	Windowed bool
	Totals   map[string]int
	Order    []string
	Filter   string // the filter the snapshot was fetched with (it can hide columns)

	// Table snapshots (a TABLE_LAYOUT view) have the column totals only; Items
	// holds the view's first rows, in its order, out of Rows.
	Table       bool
	Rows        int
	TableFields []ViewField // the view's visible fields: the table's columns
}

type SelectField struct {
	Name    string
	Options []Option
}

type Option struct {
	Name  string
	Color string
}

type RateLimit struct {
	Remaining int
	Limit     int
	ResetAt   time.Time
}

type Item struct {
	ID        string
	Kind      string // ISSUE, PULL_REQUEST, DRAFT_ISSUE, REDACTED
	Title     string
	Number    int
	Repo      string
	URL       string
	State     string // OPEN, CLOSED, MERGED, DRAFT
	Assignees []string
	Labels    []string
	UpdatedAt time.Time
	Values    map[string]string // field name -> value (single-select option, iteration, text, number, date)

	// loaded for table views only
	Milestone string
	Parent    string // parent issue, "#12 title"
	LinkedPRs []int
	SubDone   int // sub-issues progress
	SubTotal  int
}

// Ref returns a short human reference like "owner/repo#12".
func (it Item) Ref() string {
	if it.Number == 0 {
		return "draft"
	}
	return fmt.Sprintf("%s#%d", it.Repo, it.Number)
}

type Client struct {
	token  string
	source string // where the token came from, for `cardhub doctor`
	http   *http.Client
	scopes string // X-OAuth-Scopes of the last response (classic and gh tokens)
}

// ErrNoToken is returned when no GitHub token can be found anywhere.
var ErrNoToken = errors.New(`no GitHub token found.

CardHub reads your token from (first match wins):
  1. the GITHUB_TOKEN environment variable
  2. the GH_TOKEN environment variable
  3. the GitHub CLI: ` + "`gh auth token`" + `

Easiest fix, if you have the GitHub CLI (https://cli.github.com):
  gh auth login --scopes read:project,repo

Or create a classic token with the read:project and repo scopes at
https://github.com/settings/tokens and run:
  export GITHUB_TOKEN=<your token>

Run ` + "`cardhub doctor`" + ` to check your setup.`)

func NewClient() (*Client, error) {
	tok, src := os.Getenv("GITHUB_TOKEN"), "GITHUB_TOKEN"
	if tok == "" {
		tok, src = os.Getenv("GH_TOKEN"), "GH_TOKEN"
	}
	if tok == "" {
		src = "gh auth token"
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			tok = strings.TrimSpace(string(out))
		}
	}
	if tok == "" {
		return nil, ErrNoToken
	}
	return &Client{token: tok, source: src, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach GitHub (%v). Check your internet connection", err)
	}
	defer resp.Body.Close()
	c.scopes = resp.Header.Get("X-OAuth-Scopes")
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub rejected your token from %s (expired or revoked). Run `gh auth login`, or set a new GITHUB_TOKEN", c.source)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return errors.New("GitHub is rate-limiting this token. CardHub will retry; wait for the hourly reset")
	default:
		return fmt.Errorf("github: HTTP %s", resp.Status)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		e := env.Errors[0]
		if e.Type == "INSUFFICIENT_SCOPES" {
			return fmt.Errorf("your token (from %s) can't read projects: it's missing the read:project scope. "+
				"Fix: gh auth refresh --scopes read:project,repo (or create a token with read:project and repo)", c.source)
		}
		if e.Type == "NOT_FOUND" {
			return fmt.Errorf("%s. Check the owner and number in the project's URL "+
				"(github.com/orgs/<owner>/projects/<number>) and that you can open it on GitHub",
				strings.TrimSuffix(e.Message, "."))
		}
		return fmt.Errorf("github: %s", e.Message)
	}
	return json.Unmarshal(env.Data, out)
}

// itemSelection is the selection for one project item; the %s take the
// issue- and PR-only extras a table shows.
const itemSelection = `
            id type isArchived updatedAt
            fieldValues(first: 30) {
              nodes {
                ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { name } } }
                ... on ProjectV2ItemFieldIterationValue { title field { ... on ProjectV2FieldCommon { name } } }
                ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
                ... on ProjectV2ItemFieldNumberValue { number field { ... on ProjectV2FieldCommon { name } } }
                ... on ProjectV2ItemFieldDateValue { date field { ... on ProjectV2FieldCommon { name } } }
              }
            }
            content {
              ... on DraftIssue { title assignees(first: 5) { nodes { login } } }
              ... on Issue {
                title number url state
                repository { nameWithOwner }
                assignees(first: 5) { nodes { login } }
                labels(first: 5) { nodes { name } }%s
              }
              ... on PullRequest {
                title number url state isDraft
                repository { nameWithOwner }
                assignees(first: 5) { nodes { login } }
                labels(first: 5) { nodes { name } }%s
              }
            }`

var (
	// itemFields is what every board and AI link query loads per item.
	itemFields = fmt.Sprintf(itemSelection, "", "")
	// tableItemFields adds what only a table's columns show.
	tableItemFields = fmt.Sprintf(itemSelection, `
                milestone { title }
                parent { number title }
                subIssuesSummary { total completed }
                closedByPullRequestsReferences(first: 5, includeClosedPrs: true) { nodes { number } }`, `
                milestone { title }`)
)

type logins struct {
	Nodes []struct{ Login string } `json:"nodes"`
}
type names struct {
	Nodes []struct{ Name string } `json:"nodes"`
}

type rateLimitResp struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	Limit     int       `json:"limit"`
	ResetAt   time.Time `json:"resetAt"`
}

type itemNode struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	IsArchived  bool      `json:"isArchived"`
	UpdatedAt   time.Time `json:"updatedAt"`
	FieldValues struct {
		Nodes []struct {
			Name   string   `json:"name"`  // single select
			Title  string   `json:"title"` // iteration
			Text   string   `json:"text"`
			Number *float64 `json:"number"`
			Date   string   `json:"date"`
			Field  struct {
				Name string `json:"name"`
			} `json:"field"`
		} `json:"nodes"`
	} `json:"fieldValues"`
	Content *struct {
		Title      string `json:"title"`
		Number     int    `json:"number"`
		URL        string `json:"url"`
		State      string `json:"state"`
		IsDraft    bool   `json:"isDraft"`
		Repository struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"repository"`
		Assignees logins `json:"assignees"`
		Labels    names  `json:"labels"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		Parent *struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
		} `json:"parent"`
		SubIssues *struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		} `json:"subIssuesSummary"`
		LinkedPRs struct {
			Nodes []struct {
				Number int `json:"number"`
			} `json:"nodes"`
		} `json:"closedByPullRequestsReferences"`
	} `json:"content"`
}

func (n itemNode) item() Item {
	it := Item{ID: n.ID, Kind: n.Type, UpdatedAt: n.UpdatedAt, Values: map[string]string{}}
	for _, fv := range n.FieldValues.Nodes {
		if fv.Field.Name == "" {
			continue
		}
		v := fv.Name + fv.Title + fv.Text + fv.Date // a value has only one of them
		if fv.Number != nil {
			v = strconv.FormatFloat(*fv.Number, 'f', -1, 64)
		}
		it.Values[fv.Field.Name] = v
	}
	if ct := n.Content; ct != nil {
		it.Title, it.Number, it.URL, it.State = ct.Title, ct.Number, ct.URL, ct.State
		it.Repo = ct.Repository.NameWithOwner
		if ct.IsDraft {
			it.State = "DRAFT"
		}
		for _, a := range ct.Assignees.Nodes {
			it.Assignees = append(it.Assignees, a.Login)
		}
		for _, l := range ct.Labels.Nodes {
			it.Labels = append(it.Labels, l.Name)
		}
		if ct.Milestone != nil {
			it.Milestone = ct.Milestone.Title
		}
		if ct.Parent != nil {
			it.Parent = fmt.Sprintf("#%d %s", ct.Parent.Number, ct.Parent.Title)
		}
		if ct.SubIssues != nil {
			it.SubDone, it.SubTotal = ct.SubIssues.Completed, ct.SubIssues.Total
		}
		for _, pr := range ct.LinkedPRs.Nodes {
			it.LinkedPRs = append(it.LinkedPRs, pr.Number)
		}
	}
	if it.Kind == "DRAFT_ISSUE" {
		it.State = "DRAFT"
	}
	if it.Title == "" {
		it.Title = "(no access)"
	}
	return it
}

func notFound(owner string, number int) error {
	return fmt.Errorf("project %s/%d not found. Check the owner and number in its URL "+
		"(github.com/orgs/<owner>/projects/<number>), and that you can open it on GitHub", owner, number)
}

// FieldKey is how a field is named in a filter: "Release Phase" → "release-phase".
func FieldKey(field string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(field), " ", "-"))
}

// columnShown reports whether the column name of groupField survives filter's
// qualifiers on that field, the way GitHub hides a board's columns: with
// status:"A","B" only those columns show, -status:"C" hides C, no:status
// keeps only "No Status" and has:status hides it.
func columnShown(filter, groupField, name string) bool {
	if bs := orBranches(filter); len(bs) > 1 { // shown when any branch shows it
		for _, br := range bs {
			if columnShown(br, groupField, name) {
				return true
			}
		}
		return false
	}
	key, none := FieldKey(groupField), "No "+groupField
	for _, tok := range splitFilter(filter) {
		neg := strings.HasPrefix(tok, "-")
		k, v, ok := strings.Cut(strings.TrimPrefix(tok, "-"), ":")
		if !ok {
			continue
		}
		k = strings.ToLower(k)
		switch {
		case (k == "no" || k == "has") && strings.EqualFold(strings.Trim(v, `"`), key):
			isNone := name == none
			if (k == "no") == neg { // has:status or -no:status
				isNone = !isNone
			}
			if !isNone {
				return false
			}
		case k == key:
			in := false
			for _, val := range strings.Split(v, ",") {
				if strings.EqualFold(strings.Trim(val, `"`), name) {
					in = true
				}
			}
			if in == neg {
				return false
			}
		}
	}
	return true
}

// FetchColumns is the cheap board sync: one request with one aliased items
// query per column of groupField, each asking only for the first limits[col]
// cards (the ones on screen, plus a little read-ahead) and the column's exact
// total. A whole board costs about 1–2 points instead of 3 per 100 cards.
//
// With rows > 0 it syncs a table view instead: only the columns' totals (so
// moves are still seen), and the view's first rows items in project order.
func (c *Client) FetchColumns(ctx context.Context, owner string, number int, meta *ProjectMeta,
	groupField, filter string, limits map[string]int, rows int) (*Board, error) {
	b := &Board{FetchedAt: time.Now(), Title: meta.Title, URL: meta.URL, Fields: meta.Fields,
		Totals: map[string]int{}, Windowed: true, Filter: filter, Table: rows > 0}

	key := FieldKey(groupField)
	type col struct{ name, query string }
	var cols []col
	if columnShown(filter, groupField, "No "+groupField) {
		cols = append(cols, col{"No " + groupField, "no:" + key})
	}
	for _, f := range meta.Fields {
		if f.Name == groupField {
			for _, o := range f.Options {
				if columnShown(filter, groupField, o.Name) { // a view can hide columns; don't fetch them
					cols = append(cols, col{o.Name, fmt.Sprintf(`%s:"%s"`, key, strings.ReplaceAll(o.Name, `"`, `\"`))})
				}
			}
		}
	}

	// A search with OR branches runs each branch and merges them (see union).
	branches := orBranches(filter)
	if len(branches) > maxOr {
		return nil, fmt.Errorf("a search can have at most %d OR branches", maxOr)
	}
	var q, sel strings.Builder
	q.WriteString("query($owner: String!, $number: Int!")
	vars := map[string]any{"owner": owner, "number": number}
	// union asks for one list of cards (a column, or a table's rows) narrowed
	// by extra: each branch's first n cards and size, and for every mix of
	// branches the size of their overlap, which makes the merged total exact.
	union := func(alias, extra string, n int, fields string) {
		for mask := 1; mask < 1<<len(branches); mask++ {
			var parts []string
			for i, br := range branches {
				if mask&(1<<i) != 0 {
					parts = append(parts, br)
				}
			}
			name := fmt.Sprintf("%s_%d", alias, mask)
			fmt.Fprintf(&q, ", $%s: String", name)
			vars[name] = strings.TrimSpace(strings.Join(parts, " ") + " " + extra) // repeated qualifiers AND
			if bits.OnesCount(uint(mask)) == 1 && n > 0 {
				fmt.Fprintf(&sel, "    %s: items(first: %d, query: $%s) { totalCount nodes {%s\n    } }\n", name, n, name, fields)
			} else {
				fmt.Fprintf(&sel, "    %s: items(first: 0, query: $%s) { totalCount }\n", name, name)
			}
		}
	}
	if b.Table {
		union("t", "", clamp(rows, 1, 100), tableItemFields)
	}
	for i, cl := range cols {
		n, ok := limits[cl.name]
		if !ok { // column not on screen yet (first sync): visible cards plus read-ahead
			n = 2 * visibleCards
		}
		if b.Table { // a table only needs the columns' totals
			n = 0
		}
		union(fmt.Sprintf("c%d", i), cl.query, clamp(n, pick(b.Table, 0, 1), 100), itemFields)
	}
	q.WriteString(") {\n  rateLimit { cost remaining limit resetAt }\n  repositoryOwner(login: $owner) { ... on ProjectV2Owner { projectV2(number: $number) {\n")
	q.WriteString(sel.String())
	q.WriteString("  } } }\n}")

	var r struct {
		RateLimit rateLimitResp `json:"rateLimit"`
		Owner     *struct {
			Project map[string]json.RawMessage `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	if err := c.graphql(ctx, q.String(), vars, &r); err != nil {
		return nil, err
	}
	if r.Owner == nil || r.Owner.Project == nil {
		return nil, notFound(owner, number)
	}
	b.RateLimit = RateLimit{r.RateLimit.Remaining, r.RateLimit.Limit, r.RateLimit.ResetAt}
	b.Cost = r.RateLimit.Cost
	// read merges what union asked for: the cards of each branch once, in
	// branch order (a branch only once every earlier one is loaded in full, so
	// loading more never reorders them), and the union's size by
	// inclusion–exclusion.
	read := func(alias string) (items []Item, total int, err error) {
		seen, complete := map[string]bool{}, true
		for mask := 1; mask < 1<<len(branches); mask++ {
			var conn struct {
				TotalCount int        `json:"totalCount"`
				Nodes      []itemNode `json:"nodes"`
			}
			if raw, ok := r.Owner.Project[fmt.Sprintf("%s_%d", alias, mask)]; ok {
				if err := json.Unmarshal(raw, &conn); err != nil {
					return nil, 0, err
				}
			}
			ones := bits.OnesCount(uint(mask))
			total += pick(ones%2 == 1, conn.TotalCount, -conn.TotalCount)
			if ones != 1 || !complete {
				continue
			}
			for _, n := range conn.Nodes {
				if !n.IsArchived && !seen[n.ID] {
					seen[n.ID] = true
					items = append(items, n.item())
				}
			}
			complete = len(conn.Nodes) >= conn.TotalCount
		}
		return items, total, nil
	}
	if b.Table {
		items, total, err := read("t")
		if err != nil {
			return nil, err
		}
		b.Items, b.Rows = items, total
	}
	for i, cl := range cols {
		items, total, err := read(fmt.Sprintf("c%d", i))
		if err != nil {
			return nil, err
		}
		b.Order = append(b.Order, cl.name)
		b.Totals[cl.name] = total
		b.Items = append(b.Items, items...)
	}
	return b, nil
}

// FetchMatches checks the AI link's rules: one request with an aliased items
// query per rule (first 50 matches each), about 1 point however many rules.
// It ignores the view and search on screen: rules see the whole project.
func (c *Client) FetchMatches(ctx context.Context, owner string, number int, filters map[int]string) (map[int][]Item, RateLimit, error) {
	ids := make([]int, 0, len(filters))
	for id := range filters {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var q strings.Builder
	q.WriteString("query($owner: String!, $number: Int!")
	vars := map[string]any{"owner": owner, "number": number}
	for _, id := range ids {
		fmt.Fprintf(&q, ", $r%d: String", id)
		vars[fmt.Sprintf("r%d", id)] = filters[id]
	}
	q.WriteString(") {\n  rateLimit { cost remaining limit resetAt }\n  repositoryOwner(login: $owner) { ... on ProjectV2Owner { projectV2(number: $number) {\n")
	for _, id := range ids {
		fmt.Fprintf(&q, "    r%d: items(first: 50, query: $r%d) { nodes {%s\n    } }\n", id, id, itemFields)
	}
	q.WriteString("  } } }\n}")

	var r struct {
		RateLimit rateLimitResp `json:"rateLimit"`
		Owner     *struct {
			Project map[string]json.RawMessage `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	if err := c.graphql(ctx, q.String(), vars, &r); err != nil {
		return nil, RateLimit{}, err
	}
	if r.Owner == nil || r.Owner.Project == nil {
		return nil, RateLimit{}, notFound(owner, number)
	}
	out := map[int][]Item{}
	for _, id := range ids {
		var conn struct {
			Nodes []itemNode `json:"nodes"`
		}
		if raw, ok := r.Owner.Project[fmt.Sprintf("r%d", id)]; ok {
			if err := json.Unmarshal(raw, &conn); err != nil {
				return nil, RateLimit{}, err
			}
		}
		out[id] = []Item{}
		for _, n := range conn.Nodes {
			if !n.IsArchived {
				out[id] = append(out[id], n.item())
			}
		}
	}
	return out, RateLimit{r.RateLimit.Remaining, r.RateLimit.Limit, r.RateLimit.ResetAt}, nil
}

// ProjectView is a saved view of the project (the tabs in GitHub's project UI).
type ProjectView struct {
	Number  int
	Name    string
	Layout  string // BOARD_LAYOUT, TABLE_LAYOUT, ROADMAP_LAYOUT
	Filter  string
	GroupBy string      // field the view groups by (board columns), if any
	Fields  []ViewField // the fields the view shows, in order (a table's columns)
}

// ViewField is a field a view shows: its name and GitHub's data type
// (TITLE, SINGLE_SELECT, ITERATION, ASSIGNEES, LINKED_PULL_REQUESTS…).
type ViewField struct {
	Name, Type string
}

// ProjectMeta is the slow-changing part of a project: its name, single-select
// fields (and their options, i.e. the board's columns) and saved views.
type ProjectMeta struct {
	Title, URL string
	Fields     []SelectField
	Views      []ProjectView
}

const metaQuery = `
query($owner: String!, $number: Int!) {
  repositoryOwner(login: $owner) {
    ... on ProjectV2Owner {
      projectV2(number: $number) {
        title url
        fields(first: 50) {
          nodes { ... on ProjectV2SingleSelectField { name options { name color } } }
        }
        views(first: 50) {
          nodes {
            number name layout filter
            fields(first: 30) { nodes { ... on ProjectV2FieldCommon { name dataType } } }
            verticalGroupByFields(first: 1) { nodes { ... on ProjectV2FieldCommon { name } } }
            groupByFields(first: 1) { nodes { ... on ProjectV2FieldCommon { name } } }
          }
        }
      }
    }
  }
}`

func (c *Client) FetchMeta(ctx context.Context, owner string, number int) (*ProjectMeta, error) {
	type fieldNames struct {
		Nodes []struct{ Name string } `json:"nodes"`
	}
	var r struct {
		Owner *struct {
			Project *struct {
				Title  string `json:"title"`
				URL    string `json:"url"`
				Fields struct {
					Nodes []struct {
						Name    string   `json:"name"`
						Options []Option `json:"options"`
					} `json:"nodes"`
				} `json:"fields"`
				Views struct {
					Nodes []struct {
						Number int    `json:"number"`
						Name   string `json:"name"`
						Layout string `json:"layout"`
						Filter string `json:"filter"`
						Fields struct {
							Nodes []struct {
								Name     string `json:"name"`
								DataType string `json:"dataType"`
							} `json:"nodes"`
						} `json:"fields"`
						Vertical fieldNames `json:"verticalGroupByFields"`
						Group    fieldNames `json:"groupByFields"`
					} `json:"nodes"`
				} `json:"views"`
			} `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	if err := c.graphql(ctx, metaQuery, map[string]any{"owner": owner, "number": number}, &r); err != nil {
		return nil, err
	}
	if r.Owner == nil || r.Owner.Project == nil {
		return nil, notFound(owner, number)
	}
	p := r.Owner.Project
	meta := &ProjectMeta{Title: p.Title, URL: p.URL, Views: []ProjectView{}}
	for _, f := range p.Fields.Nodes {
		if f.Name != "" && len(f.Options) > 0 {
			meta.Fields = append(meta.Fields, SelectField{Name: f.Name, Options: f.Options})
		}
	}
	for _, n := range p.Views.Nodes {
		v := ProjectView{Number: n.Number, Name: n.Name, Layout: n.Layout, Filter: n.Filter}
		for _, f := range n.Fields.Nodes {
			if f.Name != "" {
				v.Fields = append(v.Fields, ViewField{f.Name, f.DataType})
			}
		}
		if len(n.Vertical.Nodes) > 0 {
			v.GroupBy = n.Vertical.Nodes[0].Name
		} else if len(n.Group.Nodes) > 0 {
			v.GroupBy = n.Group.Nodes[0].Name
		}
		meta.Views = append(meta.Views, v)
	}
	return meta, nil
}

// FindView matches a view by number or case-insensitive name; -1 if none.
func FindView(views []ProjectView, arg string) int {
	if n, err := strconv.Atoi(arg); err == nil {
		for i, v := range views {
			if v.Number == n {
				return i
			}
		}
	}
	for i, v := range views {
		if strings.EqualFold(v.Name, arg) {
			return i
		}
	}
	return -1
}

var projectURLRe = regexp.MustCompile(`github\.com/(?:orgs|users)/([^/]+)/projects/(\d+)(?:/views/(\d+))?`)

// ParseTarget accepts either a project URL or "<owner> <number>" / "<owner>/<number>".
// A URL ending in /views/N also yields that view number (0 otherwise).
func ParseTarget(args []string) (owner string, number, view int, err error) {
	if len(args) == 1 {
		if m := projectURLRe.FindStringSubmatch(args[0]); m != nil {
			n, _ := strconv.Atoi(m[2])
			v, _ := strconv.Atoi(m[3])
			return m[1], n, v, nil
		}
		if o, n, ok := strings.Cut(args[0], "/"); ok {
			args = []string{o, n}
		}
	}
	if len(args) != 2 {
		return "", 0, 0, errors.New("expected a project URL or <owner> <number>")
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n <= 0 {
		return "", 0, 0, fmt.Errorf("invalid project number %q", args[1])
	}
	return args[0], n, 0, nil
}

// ProjectRef identifies a project the viewer can open.
type ProjectRef struct {
	Owner  string
	Number int
	Title  string
}

const projectsQuery = `
query {
  viewer {
    login
    projectsV2(first: 50) { nodes { number title closed } }
    organizations(first: 50) {
      nodes { login projectsV2(first: 50) { nodes { number title closed } } }
    }
  }
}`

// ListProjects returns the open projects owned by the viewer and their organizations.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectRef, error) {
	type projects struct {
		Nodes []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Closed bool   `json:"closed"`
		} `json:"nodes"`
	}
	var r struct {
		Viewer struct {
			Login    string   `json:"login"`
			Projects projects `json:"projectsV2"`
			Orgs     struct {
				Nodes []struct {
					Login    string   `json:"login"`
					Projects projects `json:"projectsV2"`
				} `json:"nodes"`
			} `json:"organizations"`
		} `json:"viewer"`
	}
	if err := c.graphql(ctx, projectsQuery, nil, &r); err != nil {
		return nil, err
	}
	var out []ProjectRef
	add := func(owner string, ps projects) {
		for _, p := range ps.Nodes {
			if !p.Closed {
				out = append(out, ProjectRef{owner, p.Number, p.Title})
			}
		}
	}
	add(r.Viewer.Login, r.Viewer.Projects)
	for _, o := range r.Viewer.Orgs.Nodes {
		add(o.Login, o.Projects)
	}
	return out, nil
}
