package main

import (
	"context"
	"fmt"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zzet/gortex/internal/progress"
	"github.com/zzet/gortex/internal/tui"
)

// initProgress abstracts the two ways `gortex init` renders work-in-progress:
// the legacy single-line mesh Spinner (default / non-TTY) and the new
// multi-pane Dashboard (wizard path). The orchestration body in runInit calls
// into this interface so the same code drives both surfaces.
//
// The dashboard variant ignores Start (the dashboard owns its own banner) and
// treats Stage / Sub as a stage transition; the spinner variant treats Stage
// as the new top label and Sub as the sub-status, preserving today's UX for
// non-interactive callers.
type initProgress interface {
	// Start opens the surface with an initial label. No-op for the dashboard.
	Start(label string)
	// Stage advances to a new high-level stage. sub is an optional detail.
	Stage(name, sub string)
	// Sub updates the sub-status of the current stage.
	Sub(sub string)
	// StageDone marks a stage complete, optionally with a final sub-line.
	StageDone(name, sub string)
	// Reporter returns a progress.Reporter ready to attach to the indexer.
	Reporter() progress.Reporter
	// Enabled reports whether the surface is rendering an animation. Used to
	// gate the chatter-capture redirect that hides adapter logs.
	Enabled() bool
	// Done shuts down cleanly.
	Done()
	// Fail shuts down with an error mark.
	Fail(err error)
}

// spinnerProgress wraps the legacy Spinner so it satisfies initProgress.
type spinnerProgress struct{ sp *progress.Spinner }

func (s *spinnerProgress) Start(label string)       { s.sp.Start(label) }
func (s *spinnerProgress) Stage(name, sub string)   { s.sp.Set(name, sub) }
func (s *spinnerProgress) Sub(sub string)           { s.sp.Set("", sub) }
func (s *spinnerProgress) StageDone(_, sub string)  { s.sp.Set("", sub) }
func (s *spinnerProgress) Reporter() progress.Reporter { return s.sp }
func (s *spinnerProgress) Enabled() bool            { return s.sp.Enabled() }
func (s *spinnerProgress) Done()                    { s.sp.Done() }
func (s *spinnerProgress) Fail(err error)           { s.sp.Fail(err) }

// dashboardProgress wraps a running Dashboard tea.Program so it satisfies
// initProgress. Wait blocks until the program exits.
type dashboardProgress struct {
	session *initDashboardSession
	rep     progress.Reporter
}

func newDashboardProgress(s *initDashboardSession) *dashboardProgress {
	return &dashboardProgress{
		session: s,
		rep:     reporterFromSession(s),
	}
}

func (d *dashboardProgress) Start(_ string) {} // dashboard renders its own header
func (d *dashboardProgress) Stage(name, sub string) {
	if d.session != nil {
		d.session.controller.SetActive(name, sub)
	}
}
func (d *dashboardProgress) Sub(sub string) {
	if d.session != nil {
		d.session.controller.Sub(sub)
	}
}
func (d *dashboardProgress) StageDone(name, sub string) {
	if d.session != nil {
		d.session.controller.Done(name, sub)
	}
}
func (d *dashboardProgress) Reporter() progress.Reporter { return d.rep }
func (d *dashboardProgress) Enabled() bool               { return d.session != nil }
func (d *dashboardProgress) Done() {
	if d.session != nil {
		d.session.controller.Finish(nil)
		<-d.session.done
	}
}
func (d *dashboardProgress) Fail(err error) {
	if d.session != nil {
		d.session.controller.Finish(err)
		<-d.session.done
	}
}

// initDashboardStages is the high-level checklist the dashboard renders down
// the right column. Each entry maps to a single wizard "phase" — the indexer
// emits many finer-grained sub-stages that ride on top via dashReporter.
//
// Stages are added / removed dynamically by buildInitStages depending on
// which optional passes (analyze, skills) the user picked.
var (
	stageIndex    = "Index repository"
	stageAnalyze  = "Analyze codebase"
	stageSkills   = "Generate skills"
	stageAdapters = "Configure adapters"
)

// buildInitStages returns the ordered stage labels the dashboard will show
// given the per-pass flags. The orchestration code calls SetActive on each
// stage in this exact order; an empty list means "no dashboard, fall back to
// the legacy plain-text path."
func buildInitStages(analyze, skills bool) []string {
	stages := make([]string, 0, 4)
	if analyze || skills {
		stages = append(stages, stageIndex)
	}
	if analyze {
		stages = append(stages, stageAnalyze)
	}
	if skills {
		stages = append(stages, stageSkills)
	}
	stages = append(stages, stageAdapters)
	return stages
}

// initDashboardSession bundles the running tea.Program + its controller and
// the goroutine that hosts it. Closing the session cleanly tears down the
// program and waits for it to flush, so subsequent stderr writes (the agent
// summary card) start on a clean line.
type initDashboardSession struct {
	prog       *tea.Program
	controller *tui.DashboardController
	done       chan struct{}
}

// startInitDashboard spawns the dashboard tea.Program against w and returns
// a session the orchestration code uses to drive it. Returns (nil, nil) when
// w isn't a TTY — the caller falls back to the legacy Spinner.
func startInitDashboard(w io.Writer, stages []string) *initDashboardSession {
	if !progress.IsTTY(w) {
		return nil
	}
	model := tui.NewDashboard("gortex init", stages)
	prog := tea.NewProgram(model,
		tea.WithOutput(w),
		tea.WithoutSignalHandler(),
	)
	controller := tui.NewDashboardController(prog)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = prog.Run()
	}()
	return &initDashboardSession{prog: prog, controller: controller, done: done}
}

// dashReporter adapts the progress.Reporter interface onto a running dashboard
// controller. Stage names from the indexer become the *sub-label* of whatever
// the dashboard considers the active stage — the orchestration code owns the
// SetActive transitions.
type dashReporter struct {
	mu  sync.Mutex
	ctl *tui.DashboardController
}

func (r *dashReporter) Report(stage string, current, total int) {
	if r == nil || r.ctl == nil || stage == "" {
		return
	}
	sub := stage
	switch {
	case total > 0:
		sub = fmt.Sprintf("%s · %d / %d", stage, current, total)
	case current > 0:
		sub = fmt.Sprintf("%s · %d", stage, current)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctl.Sub(sub)
}

// reporterFromSession returns a progress.Reporter that drives the dashboard
// session. Returns Nop when session is nil so callers can install the reporter
// unconditionally with WithReporter.
func reporterFromSession(s *initDashboardSession) progress.Reporter {
	if s == nil || s.controller == nil {
		return progress.Nop{}
	}
	return &dashReporter{ctl: s.controller}
}

// wizardSelectedDashboard is set to true when the bubbletea wizard finishes
// successfully — selectInitProgress reads it to decide whether to start the
// multi-pane Dashboard (wizard path) or fall back to the legacy Spinner.
// It's a package-level flag rather than threading a parameter because every
// path that mutates the init globals also mutates this flag, and runInit
// reads it once just before kicking off orchestration.
var wizardSelectedDashboard bool

// selectInitProgress returns the right initProgress surface for the current
// run. Order of precedence:
//   * --no-progress: plain text spinner (no animation, no dashboard).
//   * Wizard ran successfully: dashboard.
//   * Otherwise: legacy mesh spinner (TTY-detected internally).
//
// The dashboard is only worth standing up after the wizard has cleared the
// alt-screen — otherwise it competes with the wizard's own draw cycle.
func selectInitProgress(w io.Writer) initProgress {
	if noProgress {
		sp := progress.NewSpinner(w)
		sp.Disable()
		return &spinnerProgress{sp: sp}
	}
	if wizardSelectedDashboard {
		stages := buildInitStages(initAnalyze, initSkills)
		if s := startInitDashboard(w, stages); s != nil {
			return newDashboardProgress(s)
		}
	}
	return &spinnerProgress{sp: progress.NewSpinner(w)}
}

// ensure unused-import linter doesn't trip on context until callers wire it
// back in. context is still part of the package; this keeps the import.
var _ = context.Background
