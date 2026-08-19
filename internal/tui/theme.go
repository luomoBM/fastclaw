package tui

import "github.com/charmbracelet/lipgloss"

// Adaptive colors keep the TUI readable on both light and dark
// terminal backgrounds (lipgloss picks per the detected background).
var (
	colPrimary = lipgloss.AdaptiveColor{Light: "#B8860B", Dark: "#D4A574"} // warm amber
	colAccent  = lipgloss.AdaptiveColor{Light: "#2B5FB0", Dark: "#7AA2F7"} // blue
	colSuccess = lipgloss.AdaptiveColor{Light: "#3A7D22", Dark: "#9ECE6A"}
	colError   = lipgloss.AdaptiveColor{Light: "#C4275A", Dark: "#F7768E"}
	colMuted   = lipgloss.AdaptiveColor{Light: "#8A8F98", Dark: "#565F89"}
	colDim     = lipgloss.AdaptiveColor{Light: "#B5BAC4", Dark: "#414868"}

	stylePrimary = lipgloss.NewStyle().Foreground(colPrimary)
	styleAccent  = lipgloss.NewStyle().Foreground(colAccent)
	styleSuccess = lipgloss.NewStyle().Foreground(colSuccess)
	styleError   = lipgloss.NewStyle().Foreground(colError).Bold(true)
	styleMuted   = lipgloss.NewStyle().Foreground(colMuted)
	styleDim     = lipgloss.NewStyle().Foreground(colDim)

	styleUserPrompt = lipgloss.NewStyle().Foreground(colPrimary).Bold(true)
	styleToolName   = lipgloss.NewStyle().Foreground(colAccent)
	styleSystem     = lipgloss.NewStyle().Foreground(colMuted).Italic(true)

	stylePickerBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDim).
			Padding(0, 1).
			MarginLeft(2)

	stylePickerSelected = lipgloss.NewStyle().Foreground(colPrimary).Bold(true)

	// styleTipBox frames the key-binding hints on the welcome screen.
	styleTipBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDim).
			Padding(0, 1).
			MarginLeft(3)
)
