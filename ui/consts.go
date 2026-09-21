package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var fallbackMarkStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#777777"})

var FallBackText = lipgloss.JoinVertical(lipgloss.Center, `
░█████╗░██╗░░░░░░█████╗░██╗░░░██╗██████╗░███████╗
██╔══██╗██║░░░░░██╔══██╗██║░░░██║██╔══██╗██╔════╝
██║░░╚═╝██║░░░░░███████║██║░░░██║██║░░██║█████╗░░
██║░░██╗██║░░░░░██╔══██║██║░░░██║██║░░██║██╔══╝░░
╚█████╔╝███████╗██║░░██║╚██████╔╝██████╔╝███████╗
░╚════╝░╚══════╝╚═╝░░╚═╝░╚═════╝░╚═════╝░╚══════╝
`, `
░██████╗░██████╗░██╗░░░██╗░█████╗░██████╗░
██╔════╝██╔═══██╗██║░░░██║██╔══██╗██╔══██╗
╚█████╗░██║██╗██║██║░░░██║███████║██║░░██║
░╚═══██╗╚██████╔╝██║░░░██║██╔══██║██║░░██║
██████╔╝░╚═██╔═╝░╚██████╔╝██║░░██║██████╔╝
`)

// RenderFallbackLogo adds the startup mark when it fits beside the unchanged logo.
func RenderFallbackLogo(width int) string {
	logoWidth := lipgloss.Width(FallBackText)
	mark := fallbackMarkStyle.Render("ygl")
	markWidth := lipgloss.Width(mark)
	if width < logoWidth+markWidth {
		return FallBackText
	}

	lines := strings.Split(FallBackText, "\n")
	lastVisible := len(lines) - 1
	for lastVisible >= 0 && lipgloss.Width(lines[lastVisible]) == 0 {
		lastVisible--
	}
	if lastVisible < 0 {
		return FallBackText
	}

	lines[lastVisible] += strings.Repeat(" ", logoWidth-lipgloss.Width(lines[lastVisible])) + mark
	return strings.Join(lines, "\n")
}
