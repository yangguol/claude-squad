package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRenderFallbackLogoAddsYGLWhenWide(t *testing.T) {
	rendered := RenderFallbackLogo(80)
	plain := ansi.Strip(rendered)

	require.Equal(t, strings.Count(FallBackText, "\n"), strings.Count(rendered, "\n"), "mark must not add a line")
	require.Contains(t, lastVisibleLine(plain), "ygl", "mark must appear on the final visible logo line")
	for _, line := range strings.Split(rendered, "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 80)
	}
}

func TestRenderFallbackLogoOmitsYGLWhenNarrow(t *testing.T) {
	width := lipgloss.Width(FallBackText) - 1
	rendered := RenderFallbackLogo(width)

	require.NotContains(t, ansi.Strip(rendered), "ygl")
	require.Equal(t, strings.Count(FallBackText, "\n"), strings.Count(rendered, "\n"), "mark must not wrap")
}

func TestRenderFallbackLogoMarkFitBoundary(t *testing.T) {
	logoWidth := lipgloss.Width(FallBackText)
	markWidth := lipgloss.Width(fallbackMarkStyle.Render("ygl"))

	justTooNarrow := RenderFallbackLogo(logoWidth + markWidth - 1)
	require.NotContains(t, ansi.Strip(justTooNarrow), "ygl")

	exactFit := RenderFallbackLogo(logoWidth + markWidth)
	require.Contains(t, lastVisibleLine(ansi.Strip(exactFit)), "ygl")
	require.Equal(t, logoWidth+markWidth, lipgloss.Width(exactFit))
}

func lastVisibleLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
