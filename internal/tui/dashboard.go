package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zzet/gortex/internal/progress"
)

// dashboardTick paces the mesh + active-spinner redraw at 12 fps — enough for
// smooth animation, cheap for the terminal. The mesh has 12 perimeter cells;
// one tick per ~80ms gives a one-second full revolution.
const dashboardTickInterval = 80 * time.Millisecond

type dashTickMsg time.Time

func dashTick() tea.Cmd {
	return tea.Tick(dashboardTickInterval, func(t time.Time) tea.Msg { return dashTickMsg(t) })
}

// StageState tracks one row in the dashboard's stage list.
type StageState int

const (
	StagePending StageState = iota
	StageActive
	StageDone
	StageFailed
)

// DashStage is one row in the dashboard's stage list. Sub is appended after
// the label in dim text — typically "1,338 files" or "92/267" — and updates
// live while the stage is Active.
type DashStage struct {
	Label string
	State StageState
	Sub   string
}

// DashStat is a key/value row shown in the dashboard's bottom strip (graph
// counters: nodes, edges, languages …). Values format themselves with thousand
// separators so the strip stays scannable as numbers grow.
type DashStat struct {
	Label string
	Value int64
	Unit  string // optional suffix, e.g. "files"
}

// Dashboard is a stateful multi-pane progress dashboard model: live mesh
// spinner on the left, header (title + elapsed + active label) on the right,
// stage list with ✓/⠋/◌ markers, and a stats strip across the bottom.
//
// It's a bubbletea Model so callers can wrap it in tea.NewProgram for a full
// alt-screen experience, but every mutator is also safe to call from worker
// goroutines via the tea.Program.Send channel (Set* messages below).
type Dashboard struct {
	title   string
	stages  []DashStage
	stats   []DashStat
	active  int // index of the current StageActive row (-1 = none)
	startAt time.Time

	tick     int
	width    int
	finished bool
	err      error
}

// NewDashboard builds a Dashboard with the given title and an ordered stage
// list. All stages start as StagePending; call SetActive / SetDone / SetFail
// (or send the equivalent messages to the running tea.Program) to advance.
func NewDashboard(title string, stages []string) *Dashboard {
	rows := make([]DashStage, len(stages))
	for i, s := range stages {
		rows[i] = DashStage{Label: s, State: StagePending}
	}
	return &Dashboard{
		title:   title,
		stages:  rows,
		active:  -1,
		startAt: time.Now(),
	}
}

// --- bubbletea messages -------------------------------------------------

// DashSetActiveMsg moves the cursor to the named stage and marks any prior
// active stage as done.
type DashSetActiveMsg struct {
	Stage string
	Sub   string
}

// DashSubMsg updates the sub-label of the currently active stage without
// changing which stage is active.
type DashSubMsg struct{ Sub string }

// DashDoneMsg marks the named stage done and clears the active cursor (the
// next SetActive will pick the new one).
type DashDoneMsg struct {
	Stage string
	Sub   string // optional final stat line, e.g. "1,338 files"
}

// DashFailMsg marks the named stage failed and stops the dashboard.
type DashFailMsg struct {
	Stage string
	Err   error
}

// DashStatsMsg replaces the bottom stats strip with the given rows.
type DashStatsMsg struct{ Stats []DashStat }

// DashFinishMsg stops the dashboard with a final state.
type DashFinishMsg struct {
	Err error
}

// --- bubbletea Model ---------------------------------------------------

// Init kicks off the redraw ticker.
func (d *Dashboard) Init() tea.Cmd { return dashTick() }

// Update advances state per message. Defensive: unknown messages bounce off.
func (d *Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case dashTickMsg:
		_ = m
		d.tick++
		return d, dashTick()
	case tea.WindowSizeMsg:
		d.width = m.Width
		return d, nil
	case DashSetActiveMsg:
		d.applySetActive(m.Stage, m.Sub)
		return d, nil
	case DashSubMsg:
		d.applySub(m.Sub)
		return d, nil
	case DashDoneMsg:
		d.applyDone(m.Stage, m.Sub)
		return d, nil
	case DashFailMsg:
		d.applyFail(m.Stage, m.Err)
		return d, tea.Quit
	case DashStatsMsg:
		d.stats = m.Stats
		return d, nil
	case DashFinishMsg:
		d.finishAll(m.Err)
		return d, tea.Quit
	}
	return d, nil
}

func (d *Dashboard) applySetActive(stage, sub string) {
	idx := d.findStage(stage)
	if idx < 0 {
		return
	}
	if d.active >= 0 && d.active != idx && d.stages[d.active].State == StageActive {
		d.stages[d.active].State = StageDone
	}
	d.stages[idx].State = StageActive
	d.stages[idx].Sub = sub
	d.active = idx
}

func (d *Dashboard) applySub(sub string) {
	if d.active < 0 || d.active >= len(d.stages) {
		return
	}
	d.stages[d.active].Sub = sub
}

func (d *Dashboard) applyDone(stage, sub string) {
	idx := d.findStage(stage)
	if idx < 0 {
		return
	}
	d.stages[idx].State = StageDone
	if sub != "" {
		d.stages[idx].Sub = sub
	}
	if d.active == idx {
		d.active = -1
	}
}

func (d *Dashboard) applyFail(stage string, err error) {
	idx := d.findStage(stage)
	if idx < 0 {
		idx = d.active
	}
	if idx >= 0 && idx < len(d.stages) {
		d.stages[idx].State = StageFailed
		if err != nil {
			d.stages[idx].Sub = err.Error()
		}
	}
	d.err = err
	d.finished = true
}

func (d *Dashboard) finishAll(err error) {
	for i := range d.stages {
		if d.stages[i].State == StageActive {
			if err != nil {
				d.stages[i].State = StageFailed
				if d.stages[i].Sub == "" {
					d.stages[i].Sub = err.Error()
				}
			} else {
				d.stages[i].State = StageDone
			}
		}
	}
	d.err = err
	d.finished = true
	d.active = -1
}

// View renders the dashboard as it stands. Layout:
//
//	  ● ● ●     gortex init           00:08 elapsed
//	 ●  · ·  ●  Indexing repository
//	 ●  · ●  ●
//	 ●  · ·  ●  ✓  Discover files          1,338 files
//	  ● ● ●     ✓  Parse Go                  728 files
//	            ⠋  Parse TypeScript         92/267
//	            ◌  Build call graph
//	            ◌  Generate skills
//	            ◌  Configure adapters
//
//	           nodes 18,432  ·  edges 142,876
func (d *Dashboard) View() string {
	mesh := progress.MeshLogo(d.tick)

	elapsed := time.Since(d.startAt).Truncate(time.Second).String()
	header := lipgloss.JoinHorizontal(
		lipgloss.Top,
		progress.StyleStrong.Render(d.title),
		progress.StyleHint.Render("        "+elapsed+" elapsed"),
	)
	subhead := ""
	if d.active >= 0 && d.active < len(d.stages) {
		subhead = progress.StyleSub.Render(d.stages[d.active].Label)
	} else if d.finished {
		if d.err != nil {
			subhead = progress.StyleErr.Render("failed: " + d.err.Error())
		} else {
			subhead = progress.StyleOK.Render("done")
		}
	}

	headerCol := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		subhead,
	)
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, mesh, "    ", headerCol)

	var stageRows []string
	for _, s := range d.stages {
		stageRows = append(stageRows, d.renderStageRow(s))
	}
	body := strings.Join(stageRows, "\n")

	// Indent the stage list so it lines up under the headerCol (mesh is 9
	// cells wide + 4 spaces of gutter = 13).
	body = indent(body, 13)

	stats := ""
	if len(d.stats) > 0 {
		stats = "\n" + indent(d.renderStats(), 13)
	}

	return topRow + "\n\n" + body + stats + "\n"
}

func (d *Dashboard) renderStageRow(s DashStage) string {
	var marker string
	switch s.State {
	case StagePending:
		marker = progress.StyleHint.Render("◌")
	case StageActive:
		marker = progress.StyleAccent.Render(activeSpinFrame(d.tick))
	case StageDone:
		marker = progress.StyleOK.Render("✓")
	case StageFailed:
		marker = progress.StyleErr.Render("✗")
	}
	label := s.Label
	switch s.State {
	case StageActive:
		label = progress.StyleStrong.Render(label)
	case StageDone:
		label = progress.StyleVal.Render(label)
	case StageFailed:
		label = progress.StyleErr.Render(label)
	default:
		label = progress.StyleHint.Render(label)
	}
	out := marker + "  " + padRight(label, 28)
	if s.Sub != "" {
		out += "  " + progress.StyleHint.Render(s.Sub)
	}
	return out
}

func (d *Dashboard) renderStats() string {
	var parts []string
	for _, s := range d.stats {
		v := progress.StyleStrong.Render(humanInt(s.Value))
		label := progress.StyleHint.Render(s.Label)
		unit := ""
		if s.Unit != "" {
			unit = " " + progress.StyleHint.Render(s.Unit)
		}
		parts = append(parts, label+" "+v+unit)
	}
	return progress.StyleSub.Render(strings.Join(parts, "  ·  "))
}

func (d *Dashboard) findStage(name string) int {
	for i, s := range d.stages {
		if s.Label == name {
			return i
		}
	}
	return -1
}

// braille spinner frames for the active stage marker. Same set bubbles ships
// for its "Dot" spinner; reproduced inline so we don't pull bubbles for one
// character.
var activeSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func activeSpinFrame(tick int) string {
	if tick < 0 {
		tick = -tick
	}
	return activeSpinnerFrames[tick%len(activeSpinnerFrames)]
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

func humanInt(n int64) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	// thousands separator with ','.
	digits := fmt.Sprintf("%d", n)
	out := make([]byte, 0, len(digits)+len(digits)/3)
	rem := len(digits) % 3
	if rem > 0 {
		out = append(out, digits[:rem]...)
		if len(digits) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(digits); i += 3 {
		out = append(out, digits[i:i+3]...)
		if i+3 < len(digits) {
			out = append(out, ',')
		}
	}
	return string(out)
}

// --- driving from outside the model ------------------------------------

// DashboardController is a thread-safe handle a non-bubbletea worker can use
// to push messages into a running Dashboard's tea.Program. Construct via
// NewDashboardController; the wizard's runtime stores one on its model and
// hands it to the orchestration code that previously talked to Spinner.Set.
type DashboardController struct {
	mu   sync.Mutex
	prog *tea.Program
}

// NewDashboardController returns a controller bound to the running program.
func NewDashboardController(p *tea.Program) *DashboardController {
	return &DashboardController{prog: p}
}

// SetActive marks stage as the current active row, with sub as its detail.
func (c *DashboardController) SetActive(stage, sub string) {
	c.send(DashSetActiveMsg{Stage: stage, Sub: sub})
}

// Sub updates only the sub-label of the currently active stage.
func (c *DashboardController) Sub(sub string) { c.send(DashSubMsg{Sub: sub}) }

// Done marks stage done with optional final sub-label.
func (c *DashboardController) Done(stage, sub string) {
	c.send(DashDoneMsg{Stage: stage, Sub: sub})
}

// Fail marks stage failed and shuts the dashboard down.
func (c *DashboardController) Fail(stage string, err error) {
	c.send(DashFailMsg{Stage: stage, Err: err})
}

// Stats replaces the bottom stats strip.
func (c *DashboardController) Stats(stats ...DashStat) {
	c.send(DashStatsMsg{Stats: stats})
}

// Finish stops the dashboard. Pass err = nil for a clean done; non-nil err
// marks the active stage failed before quitting.
func (c *DashboardController) Finish(err error) {
	c.send(DashFinishMsg{Err: err})
}

func (c *DashboardController) send(msg tea.Msg) {
	c.mu.Lock()
	prog := c.prog
	c.mu.Unlock()
	if prog == nil {
		return
	}
	prog.Send(msg)
}
