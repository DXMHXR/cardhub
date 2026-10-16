package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Settings the user can change from the Settings menu (S); saved between runs.
type settings struct {
	Pets bool `json:"pets"` // empty columns play a pet animation (off by default: "No Task")
}

func settingsPath() string {
	if p := statePath(); p != "" {
		return filepath.Join(filepath.Dir(p), "settings.json")
	}
	return ""
}

func loadSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func saveSettings(s settings) {
	p := settingsPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(p, b, 0o644)
}

// settingItem is one row of the Settings menu: a label and an on/off value.
type settingItem struct {
	label, help string
	on          func(settings) bool
	toggle      func(*settings)
}

var settingItems = []settingItem{
	{
		label: "Empty column animations",
		help:  `On: a little pet scene · Off: "No Task" on a grid`,
		on:    func(s settings) bool { return s.Pets },
		toggle: func(s *settings) {
			s.Pets = !s.Pets
		},
	},
}

func (m model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "s", "S", "q":
		m.settingsOpen = false
	case "up", "k":
		m.settingsCursor = max(0, m.settingsCursor-1)
	case "down", "j":
		m.settingsCursor = min(len(settingItems)-1, m.settingsCursor+1)
	case "enter", " ", "space", "left", "right", "h", "l":
		settingItems[m.settingsCursor].toggle(&m.settings)
		saveSettings(m.settings)
	}
	return m, nil
}

func (m model) settingsDialog() string {
	accent := lipgloss.Color("#388bfd")
	w := clamp(m.width-8, 30, 60)
	lines := []string{bold.Render("Settings"), ""}
	for i, it := range settingItems {
		bar, label := " ", ftLabel.Render(it.label)
		if i == m.settingsCursor {
			bar = lipgloss.NewStyle().Foreground(selBar).Bold(true).Render("┃")
			label = bold.Foreground(lipgloss.Color("#ffffff")).Render(it.label)
		}
		state := lipgloss.NewStyle().Foreground(lipgloss.Color("#3fb950")).Bold(true).Render("● On ")
		if !it.on(m.settings) {
			state = ftRule.Render("○ ") + ftLabel.Render("Off")
		}
		gap := max(2, w-6-lipgloss.Width(label)-lipgloss.Width(state))
		lines = append(lines, bar+" "+label+strings.Repeat(" ", gap)+state, "  "+dim.Render(it.help))
	}
	lines = append(lines, "", dim.Render("↑↓ choose · enter toggle · esc close"))
	body := lipgloss.NewStyle().Width(w - 4).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1).Render(body)
}

// noTask is an empty column with animations off: "No Task" over a faint grid
// that fills the space from the title rule down to the bottom border.
func noTask(w, h int) string {
	grid := lipgloss.NewStyle().Foreground(lipgloss.Color("#21262d"))
	cross, bar := gridRow(w, "┼", "─"), gridRow(w, "│", " ")
	label := " No Task "
	mid := h / 2
	lines := make([]string, h)
	for y := range lines {
		row := bar
		if y%2 == 0 {
			row = cross
		}
		if y != mid {
			lines[y] = grid.Render(row)
			continue
		}
		left := (w - len(label)) / 2
		r := []rune(row)
		lines[y] = grid.Render(string(r[:left])) +
			lipgloss.NewStyle().Foreground(lipgloss.Color("#8b949e")).Render(label) +
			grid.Render(string(r[left+len(label):]))
	}
	return strings.Join(lines, "\n")
}

// gridRow repeats a 4-wide grid cell (a joint then 3 fill characters) across w.
func gridRow(w int, joint, fill string) string {
	var b strings.Builder
	for x := 0; x < w; x++ {
		if x%4 == 0 {
			b.WriteString(joint)
		} else {
			b.WriteString(fill)
		}
	}
	return b.String()
}
