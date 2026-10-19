package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Confirmation dialogs for keys that are easy to hit by accident: Space
// (pause syncing), Q (quit), X (stop a watcher) and U (update). Only Y or Enter confirms; anything else (N,
// Esc, or the same key again: a double-tap) cancels.

type confirmKind int

const (
	confirmNone confirmKind = iota
	confirmPauseSync
	confirmQuitApp
	confirmStopWatcher // stop m.stopTarget's watcher
	confirmUpdate      // install the newer release (m.latest)
)

func (m model) confirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	kind := m.confirm
	m.confirm = confirmNone
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "y", "Y", "enter":
		switch kind {
		case confirmPauseSync:
			m.paused = true // only the board: AI watchers keep checking and reporting
		case confirmQuitApp:
			return m, tea.Quit
		case confirmStopWatcher:
			m.hub.StopWatcher(m.stopTarget)
		case confirmUpdate:
			m.updating, m.updateErr = true, nil
			return m, updateCmd(m.latest)
		}
	}
	return m, nil
}

func (m model) confirmDialog() string {
	yellow, red := lipgloss.Color("#d29922"), lipgloss.Color("#f85149")
	agent := m.hub.InUse()
	var accent lipgloss.Color
	var lines []string
	switch m.confirm {
	case confirmPauseSync:
		accent = yellow
		lines = []string{
			bold.Foreground(accent).Render("❚❚ Pause board refresh?"), "",
			ftLabel.Render("The board stops refreshing from GitHub until you press Space again. ") +
				dim.Render("R, scrolling and switching still fetch once."),
			"",
			ftLabel.Render("AI watchers keep running: ") + dim.Render("agents keep getting reports as usual."),
		}
	case confirmQuitApp:
		accent = red
		lines = []string{bold.Foreground(accent).Render("Quit CardHub?")}
		if agent {
			lines = append(lines, "", fg(yellow).Render("! ")+
				ftLabel.Render("AI agents are using CardHub's watchers. Quitting stops them all: waiting agents are told and get no more reports."))
		}
	case confirmStopWatcher:
		accent = red
		name := m.stopTarget
		var waiting bool
		for _, s := range m.hub.Snapshots() {
			if s.Board == m.stopTarget {
				name, waiting = boardLabel(s.Title, s.Board), s.Waiting
			}
		}
		lines = []string{bold.Foreground(accent).Render("Stop watching " + name + "?"), "",
			ftLabel.Render("Its rules are dropped and AI agents get no more reports for this project.")}
		if waiting {
			lines = append(lines, "", fg(yellow).Render("! ")+
				ftLabel.Render("An agent is waiting on it: it's told the watcher stopped (exit code 3)."))
		}
	case confirmUpdate:
		accent = lipgloss.Color("#3fb950")
		lines = []string{bold.Foreground(accent).Render("Update CardHub to " + m.latest + "?"), "",
			ftLabel.Render("Downloads the release from GitHub, checks it against its checksums and replaces this CardHub (" + versionLabel() + ")."),
			"", ftLabel.Render("This screen keeps running the old version until you restart CardHub.") +
				dim.Render(" Release notes: github.com/"+repoSlug+"/releases")}
		if m.updateErr != nil {
			lines = append(lines, "", errStyle.Render("Last try failed: "+m.updateErr.Error()))
		}
	}
	verb := map[confirmKind]string{confirmPauseSync: " pause", confirmQuitApp: " quit", confirmStopWatcher: " stop", confirmUpdate: " update"}[m.confirm]
	lines = append(lines, "",
		ftKey.Render("Y")+dim.Render(" / ")+ftKey.Render("Enter")+ftLabel.Render(verb)+dim.Render("    ")+
			ftKey.Render("N")+dim.Render(" / ")+ftKey.Render("Esc")+ftLabel.Render(" cancel"))
	w := clamp(m.width-8, 30, 60)
	body := lipgloss.NewStyle().Width(w - 4).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1).Render(body)
}
