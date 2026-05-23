// Package tui hosts reusable bubbletea widgets and styled blocks shared by
// every interactive `gortex` command. It composes the brand palette already
// owned by internal/progress (palette + mesh logo) with widget primitives —
// banner, checklist, dashboard — that wizards / multi-step flows snap together
// without re-deriving the visual language.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zzet/gortex/internal/progress"
)

// Banner is a header block: the gortex mesh logo on the left, a bold title on
// the top-right, and an optional dim subtitle below it. Used as the first
// line of every wizard / dashboard so the brand mark is the first thing the
// user sees. Width 0 = natural width (no border); positive width frames the
// banner in a rounded box of that width.
type Banner struct {
	Title    string
	Subtitle string
	Tick     int // animation tick — pass 0 for a static banner
	Width    int // 0 = no frame; >0 = rounded border of that width
}

// Render returns the banner as a multi-line string (no trailing newline).
func (b Banner) Render() string {
	mesh := progress.MeshLogo(b.Tick)
	right := lipgloss.JoinVertical(
		lipgloss.Left,
		"",
		progress.StyleLabel.Render(b.Title),
		progress.StyleSub.Render(b.Subtitle),
		"",
	)
	body := lipgloss.JoinHorizontal(lipgloss.Top, mesh, "    ", right)
	if b.Width <= 0 {
		return body
	}
	return progress.StyleBox.Width(b.Width - 2).Render(body)
}

// StepLine returns a "Step N of M — <heading>" line in the wizard sub-header
// style: dim "Step N of M", bold "—", bold heading. Wizards put one of these
// directly under the banner so the user always knows where they are.
func StepLine(n, total int, heading string) string {
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		progress.StyleSub.Render(fmt.Sprintf("Step %d of %d", n, total)),
		progress.StyleSub.Render("  —  "),
		progress.StyleStrong.Render(heading),
	)
}

// KeyHint renders a dim one-line keybinding hint like
// "↑/↓ move · space toggle · enter next". Pairs are joined with " · ".
func KeyHint(pairs ...string) string {
	return progress.StyleHint.Render(strings.Join(pairs, "  ·  "))
}
