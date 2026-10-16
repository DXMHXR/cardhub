package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	fbBorder = lipgloss.Color("#3d444d")
	fbTitle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#d1d7e0")).Bold(true)
)

// filterBox frames the local "/" search input in a rounded box with a title in
// its top edge. It is empty when no search is active.
func (m model) filterBox() string {
	if !m.filtering && m.filter.Value() == "" {
		return ""
	}
	inner := max(10, m.width-4)
	bc := lipgloss.NewStyle().Foreground(fbBorder)
	title := " filter "
	input := m.filter.View()
	pad := strings.Repeat(" ", max(0, inner-lipgloss.Width(input)))
	return strings.Join([]string{
		bc.Render("╭─") + fbTitle.Render(title) + bc.Render(strings.Repeat("─", max(0, inner-lipgloss.Width(title)))+"─╮"),
		bc.Render("│") + " " + input + pad + " " + bc.Render("│"),
		bc.Render("╰" + strings.Repeat("─", inner+2) + "╯"),
	}, "\n")
}
