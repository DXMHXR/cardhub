package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Confirmation dialogs for keys that are easy to hit by accident: Space
// (pause syncing) and Q (quit). Only Y or Enter confirms; anything else (N,
// Esc, or the same key again: a double-tap) cancels.

type confirmKind int

const (
	confirmNone confirmKind = iota
	confirmPauseSync
	confirmQuitApp
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
			m.paused = true
			m.notifier.SetPaused(true)
		case confirmQuitApp:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) confirmDialog() string {
	yellow, red := lipgloss.Color("#d29922"), lipgloss.Color("#f85149")
	st := m.notifier.Status()
	agent := st.Waiting || st.Rules > 0
	var accent lipgloss.Color
	var lines []string
	switch m.confirm {
	case confirmPauseSync:
		accent = yellow
		lines = []string{
			bold.Foreground(accent).Render("❚❚ Pause syncing?"), "",
			ftLabel.Render("Automatic refreshes and AI rule checks stop until you press Space again. ") +
				dim.Render("R, scrolling and switching still fetch once."),
		}
		if agent {
			lines = append(lines, "", fg(accent).Render("! ")+
				ftLabel.Render("An AI agent is using this board: it gets no new reports while paused."))
		}
	case confirmQuitApp:
		accent = red
		lines = []string{bold.Foreground(accent).Render("Quit cardhub?")}
		if agent {
			lines = append(lines, "", fg(yellow).Render("! ")+
				ftLabel.Render("An AI agent is using this board. Quitting stops the watcher: the agent is told and gets no more reports."))
		}
	}
	verb := map[confirmKind]string{confirmPauseSync: " pause", confirmQuitApp: " quit"}[m.confirm]
	lines = append(lines, "",
		ftKey.Render("Y")+dim.Render(" / ")+ftKey.Render("Enter")+ftLabel.Render(verb)+dim.Render("    ")+
			ftKey.Render("N")+dim.Render(" / ")+ftKey.Render("Esc")+ftLabel.Render(" cancel"))
	w := clamp(m.width-8, 30, 60)
	body := lipgloss.NewStyle().Width(w - 4).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1).Render(body)
}
