package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// linkView is the AI link screen (tab 3): the link's state, the rules agents
// set, recent activity, and how to connect an agent.
func (m model) linkView(h int) string {
	snap := m.notifier.Snapshot()
	grey, green, yellow := lipgloss.Color("#8b949e"), lipgloss.Color("#3fb950"), lipgloss.Color("#d29922")
	inner := m.width - 4

	// Link: is the socket open, who's listening, what was delivered
	var link []string
	accent := grey
	switch {
	case snap.Conflict != "":
		accent = yellow
		link = append(link, lipgloss.NewStyle().Foreground(yellow).Render("! ")+ftLabel.Render(snap.Conflict),
			dim.Render("Close the other cardhub on this board to let this one serve AI agents."))
	case !snap.Active:
		link = append(link, ftLabel.Render("Not serving: pick a project first."))
	default:
		agent := ftRule.Render("○") + " " + ftLabel.Render("no agent waiting")
		if snap.Waiting {
			accent = green
			agent = lipgloss.NewStyle().Foreground(green).Render("●") + " " + ftLabel.Render("an agent is waiting (events --wait)")
		}
		link = append(link,
			pair("Board", snap.Board)+"   "+agent,
			pair("Reports", fmt.Sprintf("%d sent · %d queued", snap.Sent, snap.Queued)),
			pair("Socket", trunc(snap.Path, inner-9)))
	}
	boxes := []string{boxRows([]footBox{{title: "Link", accent: accent, lines: link}}, m.width)}

	// Rules: what agents asked to hear about
	var rules []string
	if len(snap.Rules) == 0 {
		rules = append(rules, dim.Render("No rules yet. An agent adds one with: ")+ftKey.Render("cardhub notify add 'is:open label:bug'"))
	}
	for _, r := range snap.Rules {
		rules = append(rules, fmt.Sprintf("%s  %s  %s",
			ftKey.Render(fmt.Sprintf("#%d", r.ID)),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#7ee787")).Render(trunc(r.Filter, max(10, inner-34))),
			dim.Render(fmt.Sprintf("%d matching · %d reported", r.Matches, r.Reported))))
	}
	boxes = append(boxes, boxRows([]footBox{{title: fmt.Sprintf("Rules (%d)", len(snap.Rules)), accent: lipgloss.Color("#58a6ff"), lines: rules}}, m.width))

	// How to connect: shown until an agent has used the link
	if snap.Active && len(snap.Rules) == 0 && snap.Sent == 0 {
		help := []string{
			ftLabel.Render("Tell your AI agent to run:"),
			"  " + ftKey.Render("cardhub notify add '<filter>'") + dim.Render("   report cards matching a GitHub project filter"),
			"  " + ftKey.Render("cardhub events --wait --json") + dim.Render("    in the background; it wakes the agent on each report"),
			dim.Render("Quitting cardhub stops the watcher, and the waiting agent is told (exit code 3)."),
			"",
			ftLabel.Render("Or paste this sample prompt into your agent (") + ftKey.Render("C") + ftLabel.Render(" copies it):"),
		}
		quote := lipgloss.NewStyle().Foreground(lipgloss.Color("#c9d1d9"))
		for _, l := range wrapWords(samplePrompt(), max(10, inner-4)) {
			help = append(help, lipgloss.NewStyle().Foreground(lipgloss.Color("#58a6ff")).Render("▎ ")+quote.Render(l))
		}
		help = append(help, dim.Render("With several boards open, add --board <owner/number> to both commands."))
		if m.copied != "" {
			help = append(help, lipgloss.NewStyle().Foreground(green).Render("✔ "+m.copied))
		}
		boxes = append(boxes, boxRows([]footBox{{title: "Connect an agent", accent: grey, lines: help}}, m.width))
	}

	// Activity: newest first, filling the rest of the screen
	used := 0
	for _, b := range boxes {
		used += lipgloss.Height(b)
	}
	rows := max(1, h-used-2)
	var act []string
	if len(snap.Log) == 0 {
		act = append(act, dim.Render("Nothing yet."))
	}
	kindColor := map[string]lipgloss.Color{"rule": "#58a6ff", "queued": "#d29922", "sent": "#3fb950", "agent": "#a371f7", "stop": "#f85149"}
	for i, l := range snap.Log {
		if i == rows {
			break
		}
		kind := lipgloss.NewStyle().Foreground(kindColor[l.Kind]).Render(fmt.Sprintf("%-6s", l.Kind))
		act = append(act, dim.Render(l.At.Format("15:04:05"))+"  "+kind+"  "+ftLabel.Render(trunc(l.Text, inner-18)))
	}
	boxes = append(boxes, boxRows([]footBox{{title: "Activity", accent: lipgloss.Color("#a371f7"), lines: act}}, m.width))
	return strings.Join(boxes, "\n")
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
