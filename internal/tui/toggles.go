package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zzet/gortex/internal/progress"
)

// Toggle is one boolean option in an OptionsPanel. Hint is a one-line dim
// explanation shown next to the on/off chip. Detail explains the consequence
// of each pick — surfaced when the toggle is focused.
type Toggle struct {
	Label  string
	Hint   string
	Detail string
	On     bool
}

// SelectOption represents one value in a Select widget — e.g. a hook posture
// pick. Detail is shown in dim text under the focused row.
type SelectOption struct {
	Value  string
	Label  string
	Detail string
}

// Select is a single-pick widget rendered inline as a pillbox row:
// "Hook mode  ⟨ deny | enrich | nudge ⟩ — short detail line".
type Select struct {
	Label   string
	Options []SelectOption
	Picked  int
}

// Move advances the picked index by delta (wraps both ways).
func (s *Select) Move(delta int) {
	if len(s.Options) == 0 {
		return
	}
	s.Picked = (s.Picked + delta + len(s.Options)) % len(s.Options)
}

// Value returns the picked option's Value, or "" when empty.
func (s Select) Value() string {
	if len(s.Options) == 0 || s.Picked < 0 || s.Picked >= len(s.Options) {
		return ""
	}
	return s.Options[s.Picked].Value
}

// renderRow draws "  Hook mode    ⟨ deny | enrich | nudge ⟩  — detail".
func (s Select) renderRow(labelWidth int, focused bool) string {
	var parts []string
	for i, o := range s.Options {
		txt := o.Label
		switch {
		case i == s.Picked && focused:
			txt = progress.StyleAccent.Render(txt)
		case i == s.Picked:
			txt = progress.StyleStrong.Render(txt)
		default:
			txt = progress.StyleHint.Render(txt)
		}
		parts = append(parts, txt)
	}
	pill := progress.StyleSub.Render("⟨ ") +
		strings.Join(parts, progress.StyleHint.Render(" | ")) +
		progress.StyleSub.Render(" ⟩")

	label := padRight(s.Label, labelWidth)
	if focused {
		label = progress.StyleStrong.Render(label)
	} else {
		label = progress.StyleVal.Render(label)
	}
	prefix := "  "
	if focused {
		prefix = progress.StyleAccent.Render(" ▸")
	}
	detail := ""
	if focused && s.Picked >= 0 && s.Picked < len(s.Options) {
		d := s.Options[s.Picked].Detail
		if d != "" {
			detail = "  " + progress.StyleHint.Render("— "+d)
		}
	}
	return prefix + " " + label + "  " + pill + detail
}

// OptionsPanel renders an aligned column of Toggles + Selects with arrow-key
// navigation between rows and space/enter to flip the focused row. Designed
// to live below a banner and above a key-hint footer inside a wizard step.
type OptionsPanel struct {
	Toggles []Toggle
	Selects []Select

	Cursor int // row index across Toggles ⌢ Selects (toggles first)
}

// total rows = len(Toggles) + len(Selects).
func (p OptionsPanel) total() int { return len(p.Toggles) + len(p.Selects) }

// Move advances the cursor by delta, bounded to [0, total-1].
func (p *OptionsPanel) Move(delta int) {
	t := p.total()
	if t == 0 {
		return
	}
	p.Cursor += delta
	if p.Cursor < 0 {
		p.Cursor = 0
	}
	if p.Cursor >= t {
		p.Cursor = t - 1
	}
}

// ToggleCursor flips a Toggle at the cursor (no-op when the cursor sits on a
// Select). Returns whether anything changed — wizards use this to gate redraws
// on platforms that lack robust diffing.
func (p *OptionsPanel) ToggleCursor() bool {
	if p.Cursor < len(p.Toggles) {
		p.Toggles[p.Cursor].On = !p.Toggles[p.Cursor].On
		return true
	}
	return false
}

// CycleCursor advances a Select at the cursor by delta. No-op on a Toggle.
func (p *OptionsPanel) CycleCursor(delta int) bool {
	if p.Cursor >= len(p.Toggles) && p.Cursor < p.total() {
		p.Selects[p.Cursor-len(p.Toggles)].Move(delta)
		return true
	}
	return false
}

// Render returns the panel as a single block, no trailing newline.
func (p OptionsPanel) Render() string {
	labelWidth := p.maxLabelWidth()
	var rows []string
	for i, t := range p.Toggles {
		rows = append(rows, p.renderToggle(i, t, labelWidth))
	}
	for i, s := range p.Selects {
		rowIdx := len(p.Toggles) + i
		rows = append(rows, s.renderRow(labelWidth, rowIdx == p.Cursor))
	}
	return strings.Join(rows, "\n")
}

func (p OptionsPanel) renderToggle(i int, t Toggle, labelWidth int) string {
	focused := p.Cursor == i
	chipOn := progress.StyleAccent.Render("on")
	chipOff := progress.StyleHint.Render("off")
	pill := progress.StyleSub.Render("⟨ ")
	if t.On {
		pill += chipOn + progress.StyleHint.Render(" | ") + progress.StyleHint.Render("off")
	} else {
		pill += progress.StyleHint.Render("on") + progress.StyleHint.Render(" | ") + chipOff
	}
	pill += progress.StyleSub.Render(" ⟩")

	label := padRight(t.Label, labelWidth)
	if focused {
		label = progress.StyleStrong.Render(label)
	} else {
		label = progress.StyleVal.Render(label)
	}
	prefix := "  "
	if focused {
		prefix = progress.StyleAccent.Render(" ▸")
	}
	tail := ""
	if focused && t.Detail != "" {
		tail = "  " + progress.StyleHint.Render("— "+t.Detail)
	} else if t.Hint != "" {
		tail = "  " + progress.StyleHint.Render(t.Hint)
	}
	return prefix + " " + label + "  " + pill + tail
}

func (p OptionsPanel) maxLabelWidth() int {
	n := 0
	for _, t := range p.Toggles {
		n = max(n, lipgloss.Width(t.Label))
	}
	for _, s := range p.Selects {
		n = max(n, lipgloss.Width(s.Label))
	}
	return n
}
