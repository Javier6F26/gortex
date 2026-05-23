package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zzet/gortex/internal/progress"
)

// CheckItem is one row in a Checklist. Detail is shown right of the label in
// dim text. Picked starts the row in the checked state; Disabled greys it out
// and disallows toggling (used for adapters that are detected but already
// configured, etc.).
type CheckItem struct {
	Label    string
	Detail   string
	Picked   bool
	Disabled bool
}

// Checklist is a vertical multi-select list with arrow-key navigation,
// space to toggle, `a` to pick all, `n` to clear, `i` to invert. The widget
// owns no bubbletea state machinery itself — wizards step it through their
// own Update; this keeps it embeddable inside a multi-step model without
// fighting over key events.
type Checklist struct {
	Items  []CheckItem
	Cursor int // index of the focused row
}

// NewChecklist returns a checklist seeded with items. Cursor starts at the
// first non-disabled row, falling back to 0 if every row is disabled.
func NewChecklist(items []CheckItem) Checklist {
	c := Checklist{Items: items}
	for i, it := range items {
		if !it.Disabled {
			c.Cursor = i
			break
		}
	}
	return c
}

// Move shifts the cursor by delta, skipping disabled rows. Bounded — moving
// past either edge clamps. Returns the new cursor index.
func (c *Checklist) Move(delta int) int {
	if len(c.Items) == 0 {
		return 0
	}
	step := 1
	if delta < 0 {
		step = -1
	}
	idx := c.Cursor
	for n := 0; n < len(c.Items); n++ {
		idx += step
		if idx < 0 {
			idx = 0
			break
		}
		if idx >= len(c.Items) {
			idx = len(c.Items) - 1
			break
		}
		if !c.Items[idx].Disabled {
			c.Cursor = idx
			return c.Cursor
		}
	}
	c.Cursor = idx
	return c.Cursor
}

// ToggleCursor flips the picked state at the cursor (no-op on disabled).
func (c *Checklist) ToggleCursor() {
	if len(c.Items) == 0 {
		return
	}
	it := &c.Items[c.Cursor]
	if it.Disabled {
		return
	}
	it.Picked = !it.Picked
}

// PickAll sets every non-disabled row to picked.
func (c *Checklist) PickAll() {
	for i := range c.Items {
		if !c.Items[i].Disabled {
			c.Items[i].Picked = true
		}
	}
}

// PickNone clears every non-disabled row.
func (c *Checklist) PickNone() {
	for i := range c.Items {
		if !c.Items[i].Disabled {
			c.Items[i].Picked = false
		}
	}
}

// PickInvert flips every non-disabled row.
func (c *Checklist) PickInvert() {
	for i := range c.Items {
		if !c.Items[i].Disabled {
			c.Items[i].Picked = !c.Items[i].Picked
		}
	}
}

// Picked returns the labels of every checked row in original order.
func (c Checklist) Picked() []string {
	out := make([]string, 0, len(c.Items))
	for _, it := range c.Items {
		if it.Picked {
			out = append(out, it.Label)
		}
	}
	return out
}

// PickedCount returns how many rows are currently checked.
func (c Checklist) PickedCount() int {
	n := 0
	for _, it := range c.Items {
		if it.Picked {
			n++
		}
	}
	return n
}

// Render returns the visible portion of the checklist. visibleRows caps how
// many list rows the caller wants drawn; rows beyond the window are shown as
// a single "… ↑ N more" / "… ↓ N more" pointer. labelWidth lets callers align
// detail columns across rows.
func (c Checklist) Render(visibleRows, labelWidth int) string {
	if visibleRows <= 0 {
		visibleRows = len(c.Items)
	}
	if labelWidth <= 0 {
		labelWidth = c.maxLabelWidth()
	}
	start, end := c.window(visibleRows)

	var b strings.Builder
	if start > 0 {
		b.WriteString(progress.StyleHint.Render("  …  ↑ "))
		b.WriteString(progress.StyleHint.Render(plural(start, "more row")))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(c.renderRow(i, labelWidth))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if end < len(c.Items) {
		left := len(c.Items) - end
		b.WriteString("\n")
		b.WriteString(progress.StyleHint.Render("  …  ↓ "))
		b.WriteString(progress.StyleHint.Render(plural(left, "more row")))
	}
	return b.String()
}

// window returns the [start, end) slice of items to draw given the visibleRows
// cap, keeping Cursor in view.
func (c Checklist) window(visibleRows int) (int, int) {
	if len(c.Items) <= visibleRows {
		return 0, len(c.Items)
	}
	half := visibleRows / 2
	start := max(c.Cursor-half, 0)
	end := start + visibleRows
	if end > len(c.Items) {
		end = len(c.Items)
		start = end - visibleRows
	}
	return start, end
}

func (c Checklist) renderRow(i, labelWidth int) string {
	it := c.Items[i]
	mark := "[ ]"
	if it.Picked {
		mark = "[" + progress.StyleAccent.Render("x") + "]"
	}
	label := padRight(it.Label, labelWidth)
	style := progress.StyleVal
	if it.Disabled {
		style = progress.StyleHint
	}
	if i == c.Cursor {
		label = progress.StyleStrong.Render(label)
	} else {
		label = style.Render(label)
	}
	detail := ""
	if it.Detail != "" {
		detail = "  " + progress.StyleHint.Render(it.Detail)
	}
	prefix := "  "
	if i == c.Cursor {
		prefix = progress.StyleAccent.Render(" ▸")
	}
	return prefix + " " + mark + "  " + label + detail
}

func (c Checklist) maxLabelWidth() int {
	n := 0
	for _, it := range c.Items {
		n = max(n, lipgloss.Width(it.Label))
	}
	return n
}

func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func itoa(n int) string {
	// Tight stdlib-free integer-to-string used by Checklist.Render so we don't
	// drag strconv into a hot rendering path. n is always >= 0 here.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
