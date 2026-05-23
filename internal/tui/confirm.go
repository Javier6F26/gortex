package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zzet/gortex/internal/progress"
)

// ConfirmModel is a small bubbletea Model for destructive-action confirmation
// prompts. Renders: banner → optional warning card → bullet list of "what
// will happen" items → ⟨ yes | no ⟩ pillbox with the cursor on No by default
// (safer pick) → key-hint footer.
//
// Embedded inside `gortex clean`, `gortex daemon stop --force`, etc. Callers
// inspect Accepted after Run() to gate the destructive op.
type ConfirmModel struct {
	Title    string
	Subtitle string
	Warning  string   // a short red-on-default warning line ("This cannot be undone.")
	Items    []string // bullet list rendered above the yes/no pill
	YesLabel string
	NoLabel  string

	// PickedYes is the current cursor position; defaults to false (No) for
	// safety. Set true when the action is so safe that "yes" should be the
	// default focus.
	PickedYes bool

	tick      int
	accepted  bool
	cancelled bool
}

// NewConfirmModel returns a ConfirmModel with sensible defaults. Caller can
// override Title/Subtitle/Warning/Items before passing to RunConfirm.
func NewConfirmModel(title, subtitle string) *ConfirmModel {
	return &ConfirmModel{
		Title:    title,
		Subtitle: subtitle,
		YesLabel: "yes, proceed",
		NoLabel:  "no, cancel",
	}
}

type confirmTickMsg time.Time

func confirmTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(t time.Time) tea.Msg { return confirmTickMsg(t) })
}

// Init starts the mesh animation ticker so the banner glows.
func (m *ConfirmModel) Init() tea.Cmd { return confirmTick() }

// Update handles keyboard input. Enter on the focused pick commits the
// answer; Esc / Ctrl-C / q cancel (equivalent to picking No and exiting).
func (m *ConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case confirmTickMsg:
		_ = msg
		m.tick++
		return m, confirmTick()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancelled = true
			return m, tea.Quit
		case "left", "h":
			m.PickedYes = !m.PickedYes
		case "right", "l":
			m.PickedYes = !m.PickedYes
		case "tab":
			m.PickedYes = !m.PickedYes
		case "y", "Y":
			m.PickedYes = true
			m.accepted = true
			return m, tea.Quit
		case "n", "N":
			m.PickedYes = false
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			if m.PickedYes {
				m.accepted = true
			} else {
				m.cancelled = true
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

// Accepted reports whether the user picked yes (vs cancelled / picked no).
func (m *ConfirmModel) Accepted() bool { return m.accepted && !m.cancelled }

// View renders the banner + warning + bullet list + yes/no pillbox.
func (m *ConfirmModel) View() string {
	banner := Banner{
		Title:    m.Title,
		Subtitle: m.Subtitle,
		Tick:     m.tick,
	}.Render()

	var sections []string
	sections = append(sections, banner, "")

	if m.Warning != "" {
		sections = append(sections, "  "+progress.StyleErr.Render("!")+"  "+
			progress.StyleStrong.Render(m.Warning), "")
	}

	if len(m.Items) > 0 {
		sections = append(sections, "  "+progress.Heading("will remove"))
		for _, it := range m.Items {
			sections = append(sections, "    "+progress.StyleHint.Render("·")+"  "+progress.StyleVal.Render(it))
		}
		sections = append(sections, "")
	}

	yes := m.YesLabel
	no := m.NoLabel
	if m.PickedYes {
		yes = progress.StyleAccent.Render(yes)
		no = progress.StyleHint.Render(no)
	} else {
		yes = progress.StyleHint.Render(yes)
		no = progress.StyleStrong.Render(no)
	}
	pill := progress.StyleSub.Render("⟨ ") + yes + progress.StyleHint.Render("  |  ") + no + progress.StyleSub.Render(" ⟩")
	sections = append(sections, "  "+pill, "")

	sections = append(sections,
		KeyHint("←/→ pick", "y / n shortcut", "enter confirm", "esc cancel"),
	)
	return lipgloss.JoinVertical(lipgloss.Left, sections...) + "\n"
}
