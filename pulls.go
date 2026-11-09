package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// GitHub side of the Pull requests screen: the repos to pick from, a repo's
// pull requests, and merging one.

// PR is one pull request as the list and its detail popup show it.
type PR struct {
	ID, URL        string
	Number         int
	Title          string
	State          string // OPEN, CLOSED, MERGED
	Draft          bool
	Author         string
	Head, Base     string // branch names
	Labels         []string
	Assignees      []string
	Reviews        []string // "@login approved", latest review per reviewer
	ReviewDecision string   // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, or ""
	Mergeable      string   // MERGEABLE, CONFLICTING, UNKNOWN
	Checks         string   // SUCCESS, FAILURE, PENDING, ERROR, EXPECTED, or "" (none)
	Additions      int
	Deletions      int
	Files          int
	Comments       int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	MergedAt       time.Time
	ClosedAt       time.Time
	MergedBy       string
}

// RepoPRs is one page of a repo's pull requests plus what merging needs to know.
type RepoPRs struct {
	Repo                string
	PRs                 []PR
	Open, Closed        int // totals; Closed counts merged ones too
	MergeCommit, Squash bool
	Rebase              bool
	CanMerge            bool // viewer has write access
	RateLimit           RateLimit
	FetchedAt           time.Time
}

// prPage is how many pull requests the screen loads per state.
const prPage = 30

const prsQuery = `
query($owner: String!, $name: String!, $states: [PullRequestState!], $n: Int!, $open: Boolean!) {
  rateLimit { cost remaining limit resetAt }
  repository(owner: $owner, name: $name) {
    nameWithOwner viewerPermission
    mergeCommitAllowed squashMergeAllowed rebaseMergeAllowed
    open: pullRequests(states: [OPEN], first: 0) { totalCount }
    closed: pullRequests(states: [CLOSED, MERGED], first: 0) { totalCount }
    pullRequests(states: $states, first: $n, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes {
        id url number title state isDraft
        author { login }
        headRefName baseRefName
        reviewDecision mergeable @include(if: $open)
        additions deletions changedFiles
        createdAt updatedAt mergedAt closedAt
        mergedBy { login }
        comments { totalCount }
        labels(first: 5) { nodes { name } }
        assignees(first: 5) { nodes { login } }
        latestReviews(first: 10) { nodes { state author { login } } }
        commits(last: 1) @include(if: $open) { nodes { commit { statusCheckRollup { state } } } }
      }
    }
  }
}`

// FetchPRs loads a repo's ("owner/name") open, or closed and merged, pull
// requests, most recently updated first.
func (c *Client) FetchPRs(ctx context.Context, repo string, closed bool) (*RepoPRs, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("%q isn't a repo: expected owner/name", repo)
	}
	states := []string{"OPEN"}
	if closed {
		states = []string{"CLOSED", "MERGED"}
	}
	type login struct {
		Login string `json:"login"`
	}
	type total struct {
		TotalCount int `json:"totalCount"`
	}
	var r struct {
		RateLimit rateLimitResp `json:"rateLimit"`
		Repo      *struct {
			NameWithOwner string `json:"nameWithOwner"`
			Permission    string `json:"viewerPermission"`
			MergeCommit   bool   `json:"mergeCommitAllowed"`
			Squash        bool   `json:"squashMergeAllowed"`
			Rebase        bool   `json:"rebaseMergeAllowed"`
			Open          total  `json:"open"`
			Closed        total  `json:"closed"`
			PRs           struct {
				Nodes []struct {
					ID             string    `json:"id"`
					URL            string    `json:"url"`
					Number         int       `json:"number"`
					Title          string    `json:"title"`
					State          string    `json:"state"`
					IsDraft        bool      `json:"isDraft"`
					Author         *login    `json:"author"`
					Head           string    `json:"headRefName"`
					Base           string    `json:"baseRefName"`
					ReviewDecision string    `json:"reviewDecision"`
					Mergeable      string    `json:"mergeable"`
					Additions      int       `json:"additions"`
					Deletions      int       `json:"deletions"`
					Files          int       `json:"changedFiles"`
					CreatedAt      time.Time `json:"createdAt"`
					UpdatedAt      time.Time `json:"updatedAt"`
					MergedAt       time.Time `json:"mergedAt"`
					ClosedAt       time.Time `json:"closedAt"`
					MergedBy       *login    `json:"mergedBy"`
					Comments       total     `json:"comments"`
					Labels         names     `json:"labels"`
					Assignees      logins    `json:"assignees"`
					Reviews        struct {
						Nodes []struct {
							State  string `json:"state"`
							Author *login `json:"author"`
						} `json:"nodes"`
					} `json:"latestReviews"`
					Commits struct {
						Nodes []struct {
							Commit struct {
								Rollup *struct {
									State string `json:"state"`
								} `json:"statusCheckRollup"`
							} `json:"commit"`
						} `json:"nodes"`
					} `json:"commits"`
				} `json:"nodes"`
			} `json:"pullRequests"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "states": states, "n": prPage, "open": !closed}
	if err := c.graphql(ctx, prsQuery, vars, &r); err != nil {
		if strings.Contains(err.Error(), "Could not resolve to a Repository") {
			return nil, fmt.Errorf("no repo %s, or your token can't read it. P picks another", repo)
		}
		return nil, err
	}
	if r.Repo == nil {
		return nil, fmt.Errorf("no repo %s, or your token can't read it", repo)
	}
	who := func(l *login) string {
		if l == nil || l.Login == "" {
			return "ghost"
		}
		return l.Login
	}
	out := &RepoPRs{
		Repo: r.Repo.NameWithOwner, Open: r.Repo.Open.TotalCount, Closed: r.Repo.Closed.TotalCount,
		MergeCommit: r.Repo.MergeCommit, Squash: r.Repo.Squash, Rebase: r.Repo.Rebase,
		CanMerge:  r.Repo.Permission == "WRITE" || r.Repo.Permission == "MAINTAIN" || r.Repo.Permission == "ADMIN",
		RateLimit: RateLimit{r.RateLimit.Remaining, r.RateLimit.Limit, r.RateLimit.ResetAt}, FetchedAt: time.Now(),
	}
	for _, n := range r.Repo.PRs.Nodes {
		p := PR{
			ID: n.ID, URL: n.URL, Number: n.Number, Title: n.Title, State: n.State, Draft: n.IsDraft,
			Author: who(n.Author), Head: n.Head, Base: n.Base,
			ReviewDecision: n.ReviewDecision, Mergeable: n.Mergeable,
			Additions: n.Additions, Deletions: n.Deletions, Files: n.Files, Comments: n.Comments.TotalCount,
			CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, MergedAt: n.MergedAt, ClosedAt: n.ClosedAt,
		}
		if n.MergedBy != nil {
			p.MergedBy = n.MergedBy.Login
		}
		for _, l := range n.Labels.Nodes {
			p.Labels = append(p.Labels, l.Name)
		}
		for _, a := range n.Assignees.Nodes {
			p.Assignees = append(p.Assignees, a.Login)
		}
		for _, rv := range n.Reviews.Nodes {
			p.Reviews = append(p.Reviews, "@"+who(rv.Author)+" "+reviewLabel(rv.State))
		}
		if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.Rollup != nil {
			p.Checks = n.Commits.Nodes[0].Commit.Rollup.State
		}
		out.PRs = append(out.PRs, p)
	}
	return out, nil
}

func reviewLabel(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "requested changes"
	case "COMMENTED":
		return "commented"
	case "DISMISSED":
		return "dismissed"
	}
	return strings.ToLower(state)
}

const reposQuery = `
query {
  viewer {
    repositories(first: 100, orderBy: {field: PUSHED_AT, direction: DESC},
                 affiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER],
                 ownerAffiliations: [OWNER, COLLABORATOR, ORGANIZATION_MEMBER]) {
      nodes { nameWithOwner isArchived }
    }
  }
}`

// ListRepos returns the viewer's 100 most recently pushed repos (their own,
// ones they collaborate on and their organizations'), skipping archived ones.
func (c *Client) ListRepos(ctx context.Context) ([]string, error) {
	var r struct {
		Viewer struct {
			Repos struct {
				Nodes []struct {
					Name     string `json:"nameWithOwner"`
					Archived bool   `json:"isArchived"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"viewer"`
	}
	if err := c.graphql(ctx, reposQuery, nil, &r); err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range r.Viewer.Repos.Nodes {
		if !n.Archived {
			out = append(out, n.Name)
		}
	}
	return out, nil
}

// MergeMethod is how a pull request gets merged.
type MergeMethod string

const (
	MergeCommit MergeMethod = "MERGE"
	MergeSquash MergeMethod = "SQUASH"
	MergeRebase MergeMethod = "REBASE"
)

func (mm MergeMethod) Label() string {
	return map[MergeMethod]string{MergeCommit: "Create a merge commit", MergeSquash: "Squash and merge", MergeRebase: "Rebase and merge"}[mm]
}

// Methods lists the merge methods the repo allows, in GitHub's order.
func (r *RepoPRs) Methods() []MergeMethod {
	var out []MergeMethod
	if r.MergeCommit {
		out = append(out, MergeCommit)
	}
	if r.Squash {
		out = append(out, MergeSquash)
	}
	if r.Rebase {
		out = append(out, MergeRebase)
	}
	return out
}

const mergeMutation = `
mutation($id: ID!, $method: PullRequestMergeMethod!) {
  mergePullRequest(input: {pullRequestId: $id, mergeMethod: $method}) {
    pullRequest { state }
  }
}`

// MergePR merges a pull request (by node ID) the given way.
func (c *Client) MergePR(ctx context.Context, id string, method MergeMethod) error {
	var r struct {
		Merge struct {
			PR struct {
				State string `json:"state"`
			} `json:"pullRequest"`
		} `json:"mergePullRequest"`
	}
	if err := c.graphql(ctx, mergeMutation, map[string]any{"id": id, "method": string(method)}, &r); err != nil {
		return err
	}
	if r.Merge.PR.State != "MERGED" {
		return fmt.Errorf("GitHub didn't merge it (state: %s)", strings.ToLower(r.Merge.PR.State))
	}
	return nil
}

// boardRepos lists the repos the board's cards come from, most cards first.
func boardRepos(b *Board) []string {
	if b == nil {
		return nil
	}
	n := map[string]int{}
	for _, it := range b.Items {
		if it.Repo != "" {
			n[it.Repo]++
		}
	}
	out := make([]string, 0, len(n))
	for r := range n {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if n[out[i]] != n[out[j]] {
			return n[out[i]] > n[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

const projectReposQuery = `
query($owner: String!, $number: Int!) {
  repositoryOwner(login: $owner) {
    ... on ProjectV2Owner {
      projectV2(number: $number) {
        repositories(first: 50) { nodes { nameWithOwner } }
        items(first: 100, orderBy: {field: POSITION, direction: ASC}) {
          nodes {
            content {
              ... on Issue { repository { nameWithOwner } }
              ... on PullRequest { repository { nameWithOwner } }
            }
          }
        }
      }
    }
  }
}`

// ProjectRepos lists the repos a project works on: the ones its cards come
// from (most cards first, among its first 100) and the ones linked to it.
func (c *Client) ProjectRepos(ctx context.Context, owner string, number int) ([]string, error) {
	type repo struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	var r struct {
		Owner *struct {
			Project *struct {
				Repos struct {
					Nodes []repo `json:"nodes"`
				} `json:"repositories"`
				Items struct {
					Nodes []struct {
						Content *struct {
							Repository *repo `json:"repository"`
						} `json:"content"`
					} `json:"nodes"`
				} `json:"items"`
			} `json:"projectV2"`
		} `json:"repositoryOwner"`
	}
	if err := c.graphql(ctx, projectReposQuery, map[string]any{"owner": owner, "number": number}, &r); err != nil {
		return nil, err
	}
	if r.Owner == nil || r.Owner.Project == nil {
		return nil, notFound(owner, number)
	}
	b := &Board{}
	for _, n := range r.Owner.Project.Items.Nodes {
		if n.Content != nil && n.Content.Repository != nil {
			b.Items = append(b.Items, Item{Repo: n.Content.Repository.NameWithOwner})
		}
	}
	out := boardRepos(b)
	for _, l := range r.Owner.Project.Repos.Nodes {
		if !slices.Contains(out, l.NameWithOwner) {
			out = append(out, l.NameWithOwner)
		}
	}
	return out, nil
}
