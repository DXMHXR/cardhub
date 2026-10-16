package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ---- last project: plain `cardhub` reopens what you watched last ----

type lastState struct {
	Owner  string `json:"owner"`
	Number int    `json:"number"`
	View   string `json:"view,omitempty"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "cardhub", "last.json")
}

func loadLast() (lastState, bool) {
	var s lastState
	b, err := os.ReadFile(statePath())
	if err != nil || json.Unmarshal(b, &s) != nil || s.Owner == "" || s.Number <= 0 {
		return lastState{}, false
	}
	return s, true
}

func saveLast(s lastState) {
	p := statePath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

// ---- cardhub doctor: check everything cardhub needs, say how to fix what's missing ----

var (
	okMark   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Bold(true).Render("✔")
	warnMark = lipgloss.NewStyle().Foreground(lipgloss.Color("#d29922")).Bold(true).Render("!")
	failMark = lipgloss.NewStyle().Foreground(lipgloss.Color("#f85149")).Bold(true).Render("✘")
	hint     = lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e"))
)

func check(mark, title string, detail ...string) {
	fmt.Printf(" %s %s\n", mark, title)
	for _, d := range detail {
		fmt.Println(hint.Render("     " + d))
	}
}

// runDoctor returns the process exit code: 0 when cardhub can run.
func runDoctor() int {
	fmt.Println(bold.Render("CardHub doctor") + hint.Render(" — checking what CardHub needs to run"))
	fmt.Println()

	if _, err := exec.LookPath("gh"); err == nil {
		check(okMark, "GitHub CLI installed", "optional: CardHub only uses it to read your login token")
	} else {
		check(hint.Render("·"), "GitHub CLI not installed (optional)",
			"CardHub works without it if GITHUB_TOKEN is set. To use it: https://cli.github.com")
	}

	c, err := NewClient()
	if err != nil {
		check(failMark, "GitHub token")
		fmt.Println()
		fmt.Println(indent(err.Error(), "   "))
		return 1
	}
	check(okMark, "GitHub token found", "from "+c.source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var r struct {
		Viewer    struct{ Login string } `json:"viewer"`
		RateLimit rateLimitResp          `json:"rateLimit"`
	}
	if err := c.graphql(ctx, `query { viewer { login } rateLimit { remaining limit resetAt } }`, nil, &r); err != nil {
		check(failMark, "Connect to GitHub", err.Error())
		return 1
	}
	check(okMark, "Signed in as "+r.Viewer.Login,
		fmt.Sprintf("API budget %d/%d left, resets in %s", r.RateLimit.Remaining, r.RateLimit.Limit,
			untilLabel(time.Until(r.RateLimit.ResetAt))))

	exit := 0
	scopes := map[string]bool{}
	for _, s := range strings.Split(c.scopes, ",") {
		scopes[strings.TrimSpace(s)] = true
	}
	switch {
	case c.scopes == "" && strings.HasPrefix(c.token, "github_pat_"):
		check(warnMark, "Fine-grained token: permissions can't be listed",
			"needs Organization → Projects: Read, and Repository → Issues, Pull requests, Metadata: Read",
			"(an org may also have to approve fine-grained tokens). Checking access below…")
	case scopes["project"] || scopes["read:project"]:
		check(okMark, "Can read projects", "scope: "+pick(scopes["project"], "project", "read:project"))
	default:
		check(failMark, "Can't read projects: token is missing the read:project scope",
			"fix: gh auth refresh --scopes read:project,repo",
			"   or create a classic token with read:project + repo at https://github.com/settings/tokens")
		exit = 1
	}
	if c.scopes != "" {
		if scopes["repo"] {
			check(okMark, "Can read private repositories", "cards from private repos show their titles")
		} else {
			check(warnMark, "No repo scope: cards from private repositories show as \"(no access)\"",
				"fix: gh auth refresh --scopes read:project,repo")
		}
		if !scopes["read:org"] && !scopes["admin:org"] {
			check(warnMark, "No read:org scope: organization projects may be missing from the P menu",
				"fix: gh auth refresh --scopes read:org")
		}
	}

	projects, err := c.ListProjects(ctx)
	switch {
	case err != nil:
		check(failMark, "List your projects", err.Error())
		exit = 1
	case len(projects) == 0:
		check(warnMark, "No open projects found for "+r.Viewer.Login,
			"open one directly instead: cardhub https://github.com/orgs/<org>/projects/<number>")
	default:
		var names []string
		for i, p := range projects {
			if i == 5 {
				names = append(names, fmt.Sprintf("… and %d more", len(projects)-5))
				break
			}
			names = append(names, fmt.Sprintf("%s #%d  %s", p.Owner, p.Number, p.Title))
		}
		check(okMark, fmt.Sprintf("%d open projects you can watch", len(projects)), names...)
	}

	fmt.Println()
	if exit == 0 {
		fmt.Println(" " + okMark + bold.Render(" Ready.") + hint.Render(" Run `cardhub` to pick a project, or `cardhub <project-url>`."))
	} else {
		fmt.Println(" " + failMark + bold.Render(" Not ready yet.") + hint.Render(" Fix the ✘ items above, then run `cardhub doctor` again."))
	}
	return exit
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func indent(s, pad string) string {
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}
