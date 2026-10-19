package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Teaching AI agents to use CardHub: `cardhub guide` prints the manual for the
// installed version, and `cardhub skill install` drops a small skill into
// every agent CLI found on this machine that points the agent at the guide.
// The skill never goes stale: whatever it says, the guide comes from the
// binary that's running.

const guideText = `CardHub %s — guide for AI agents

CardHub is a live terminal dashboard for a GitHub Projects board that a human
keeps open. Through its AI link you ask it to report card changes, then wait
for them without polling: waiting costs no tokens until something happens.
CardHub only reads the board; change cards with your own tools (gh).

The loop
  1. cardhub notify add '<filter>' --board "<project>" --no-open
       Report cards matching a GitHub project filter, e.g. 'is:open label:bug'
       or 'label:needs-triage status:Todo'. Each card is reported once; cards
       that already match are reported straight away. Adding the same filter
       again is harmless.
  2. cardhub events --wait --json --board "<project>"
       Run it IN THE BACKGROUND (your harness wakes you when it exits). It
       blocks until there are reports, prints one JSON object per line
       (number, title, status, labels, url, rule) and exits.
  3. Handle each reported card, oldest first, then go back to step 2.
     Reports that arrive while nobody waits are queued for the next call.

Rules
  - Don't start cardhub (the TUI) yourself; the human runs it. --no-open makes
    notify add fail with exit 4 instead of opening a new CardHub window.
  - Only one agent can wait per project at a time.
  - Rules live in the open CardHub. If it's closed (exit 3 or 4), its rules are
    gone: stop and tell the human. Once they reopen it, add the rules again.
  - --board takes the project title (a unique part works), owner/number or
    URL. You can leave it out while only one project is open.

Other commands
  cardhub notify list            rules, match counts, queued reports, waiting agent
  cardhub notify remove <id>     stop a rule
  cardhub events                 print queued reports now, without waiting
  cardhub doctor                 check the token and access if something fails

Exit codes
  0 ok · 1 error · 2 usage (e.g. several boards open: pass --board)
  3 the watcher stopped (CardHub quit) · 4 CardHub isn't open
`

func runGuide() int {
	fmt.Printf(guideText, versionLabel())
	return exitOK
}

// skillMD is the skill every agent CLI gets: when to use CardHub, and to read
// the guide for how.
const skillMD = `---
name: cardhub
description: React to changes on a GitHub Projects board through CardHub, the board dashboard the user keeps open. Use when asked to watch a project board, wait for new or moved cards (e.g. new bugs from QA), or act when a card matches a filter, instead of polling GitHub on a timer.
---

# CardHub

Before using CardHub, run ` + "`cardhub guide`" + ` and follow it: it is the manual for
the installed version (commands, the wait loop, exit codes).

In short: ` + "`cardhub notify add '<filter>' --no-open`" + `, then
` + "`cardhub events --wait --json`" + ` in the background, handle what it reports, repeat.
Never start the ` + "`cardhub`" + ` dashboard yourself; the user runs it.
`

// skillHome is an agent CLI that loads skills from dir/<name>/SKILL.md; the
// skill is installed only when the CLI's home folder exists.
type skillHome struct{ agent, home, dir string }

func skillHomes() []skillHome {
	h, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []skillHome{
		{"Claude Code", filepath.Join(h, ".claude"), filepath.Join(h, ".claude", "skills")},
		{"Codex", filepath.Join(h, ".codex"), filepath.Join(h, ".codex", "skills")},
		{"shared agent skills", filepath.Join(h, ".agents"), filepath.Join(h, ".agents", "skills")},
		{"opencode", filepath.Join(h, ".config", "opencode"), filepath.Join(h, ".config", "opencode", "skills")},
		{"Cursor", filepath.Join(h, ".cursor"), filepath.Join(h, ".cursor", "skills")},
	}
}

const skillUsage = `cardhub skill: teach AI agent CLIs to use CardHub

  cardhub skill install     add the cardhub skill to every agent CLI found here
                            (Claude Code, Codex, opencode, Cursor, ~/.agents)
  cardhub skill uninstall   remove it again
  cardhub skill show        print the skill and where it's installed

Agents without skills: add "Before using CardHub, run ` + "`cardhub guide`" + `."
to their instructions file (AGENTS.md, GEMINI.md, …).
`

func runSkill(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, skillUsage)
		return exitUsage
	}
	switch args[0] {
	case "install":
		return installSkill()
	case "uninstall":
		return uninstallSkill()
	case "show":
		fmt.Print(skillMD)
		fmt.Println()
		for _, s := range skillHomes() {
			p := filepath.Join(s.dir, "cardhub", "SKILL.md")
			if _, err := os.Stat(p); err == nil {
				fmt.Printf("installed for %s: %s\n", s.agent, p)
			}
		}
		return exitOK
	case "help", "-h", "--help":
		fmt.Print(skillUsage)
		return exitOK
	}
	fmt.Fprint(os.Stderr, skillUsage)
	return exitUsage
}

func installSkill() int {
	n := 0
	for _, s := range skillHomes() {
		if _, err := os.Stat(s.home); err != nil {
			continue // that agent CLI isn't installed
		}
		dir := filepath.Join(s.dir, "cardhub")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "✘ %s: %v\n", s.agent, err)
			continue
		}
		p := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(p, []byte(skillMD), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "✘ %s: %v\n", s.agent, err)
			continue
		}
		fmt.Printf("✔ %-20s %s\n", s.agent, p)
		n++
	}
	if n == 0 {
		fmt.Println("No agent CLI with skills found (looked for ~/.claude, ~/.codex, ~/.agents, ~/.config/opencode, ~/.cursor).")
	}
	fmt.Println("\nOther agents: add this line to their instructions file (AGENTS.md, GEMINI.md, …):")
	fmt.Println("  Before using CardHub, run `cardhub guide`.")
	if n == 0 {
		return exitError
	}
	return exitOK
}

func uninstallSkill() int {
	for _, s := range skillHomes() {
		dir := filepath.Join(s.dir, "cardhub")
		b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		if err != nil {
			continue
		}
		if !strings.Contains(string(b), "name: cardhub") { // not ours: leave it
			fmt.Fprintf(os.Stderr, "skipped %s: %s isn't CardHub's skill\n", s.agent, dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "✘ %s: %v\n", s.agent, err)
			continue
		}
		fmt.Printf("removed for %s: %s\n", s.agent, dir)
	}
	return exitOK
}
