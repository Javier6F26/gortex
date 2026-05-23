package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zzet/gortex/internal/tui"
)

// TestWizardSnapshotAtEachStep prints the wizard view at every step. It's a
// smoke test that the layout doesn't blow up — not a golden file comparison
// (those are notoriously fragile under ANSI styling changes). If you want to
// eyeball the output, set GORTEX_DUMP_WIZARD=<file> and read the file
// afterwards.
func TestWizardSnapshotAtEachStep(t *testing.T) {
	dumpPath := os.Getenv("GORTEX_DUMP_WIZARD")

	m := newInitWizardModel(".", sampleRegistered(), sampleDetected(), defaultDefaults())

	var dumpBuf strings.Builder
	for _, step := range []wizardStep{stepAgents, stepOptions, stepConfirm} {
		m.step = step
		out := m.View()
		assert.NotEmpty(t, out, "view at step %d must not be empty", step)
		assert.Contains(t, out, "gortex init", "banner must always be present")
		dumpBuf.WriteString("\n=== step ")
		dumpBuf.WriteString(stepName(step))
		dumpBuf.WriteString(" ===\n")
		dumpBuf.WriteString(out)
	}
	if dumpPath != "" {
		_ = os.WriteFile(dumpPath, []byte(dumpBuf.String()), 0o600)
	}
}

func stepName(s wizardStep) string {
	switch s {
	case stepAgents:
		return "agents"
	case stepOptions:
		return "options"
	case stepConfirm:
		return "confirm"
	}
	return "unknown"
}

// TestInstallWizardBannerSaysInstall verifies the install flavour swaps both
// the banner title and the subtitle so `gortex install -i` never looks like
// a leaked `gortex init` screen.
func TestInstallWizardBannerSaysInstall(t *testing.T) {
	m := newInstallWizardModel("/Users/demo", sampleRegistered(), sampleDetected(), defaultDefaults())

	view := m.View()
	assert.Contains(t, view, "gortex install", "install wizard banner title must say 'gortex install'")
	assert.NotContains(t, view, "gortex init", "install wizard must NOT echo the init title")
	assert.Contains(t, view, "your machine", "install subtitle target must be 'your machine'")
}

// TestDashboardRendersWithoutPanic builds a dashboard, drives a few transitions,
// and verifies the rendered view contains the expected markers.
func TestDashboardRendersWithoutPanic(t *testing.T) {
	d := tui.NewDashboard("gortex init", []string{
		"Index repository",
		"Configure adapters",
	})

	v0 := d.View()
	assert.Contains(t, v0, "gortex init")
	assert.Contains(t, v0, "Index repository")
	assert.Contains(t, v0, "Configure adapters")

	// Walk through a normal lifecycle by feeding messages directly.
	d.Update(tui.DashSetActiveMsg{Stage: "Index repository", Sub: "walking files"})
	v1 := d.View()
	assert.Contains(t, v1, "walking files", "active stage sub must render")

	d.Update(tui.DashDoneMsg{Stage: "Index repository", Sub: "1,338 files"})
	v2 := d.View()
	assert.Contains(t, v2, "1,338 files", "done sub-label must persist")
	assert.Contains(t, v2, "✓", "done marker must render")

	d.Update(tui.DashSetActiveMsg{Stage: "Configure adapters", Sub: "claude-code"})
	d.Update(tui.DashStatsMsg{Stats: []tui.DashStat{{Label: "agents", Value: 7}}})
	v3 := d.View()
	assert.Contains(t, v3, "agents", "stats strip must render")
	assert.Contains(t, v3, "7")

	d.Update(tui.DashFinishMsg{Err: nil})
	v4 := d.View()
	assert.Contains(t, v4, "done", "finish state must render 'done'")
	if strings.Contains(v4, "failed") {
		t.Fatalf("clean finish should not say failed:\n%s", v4)
	}
}
