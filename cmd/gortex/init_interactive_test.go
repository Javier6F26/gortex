package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// runWizard is a test harness that feeds canned stdin into
// runInteractiveInit. Returns the choice + the prompt output so
// assertions can pin both the control flow and the user-facing text.
func runWizard(t *testing.T, input string) (interactiveChoice, string) {
	t.Helper()
	var out bytes.Buffer
	in := strings.NewReader(input)
	choice, _ := runInteractiveInit(in, &out, false)
	return choice, out.String()
}

func TestInteractiveWizard_DefaultHooksYes(t *testing.T) {
	// Pressing Enter at the hooks prompt defaults to yes.
	choice, out := runWizard(t, "\n")
	assert.True(t, choice.Hooks, "empty input must default to yes")
	assert.Contains(t, out, "Install Claude Code hooks")
}

func TestInteractiveWizard_DeclineHooks(t *testing.T) {
	// Explicit n declines hooks.
	choice, _ := runWizard(t, "n\n")
	assert.False(t, choice.Hooks, "'n' must set Hooks=false")
}

func TestInteractiveWizard_HooksPresetSkipsPrompt(t *testing.T) {
	// When --hooks / --no-hooks was already passed on the CLI, the wizard
	// must not re-ask. We feed no input — if the hooks prompt fired,
	// reader.ReadString would return EOF and ok would be false.
	var out bytes.Buffer
	in := strings.NewReader("")
	choice, ok := runInteractiveInit(in, &out, true)
	assert.True(t, ok, "preset path must return ok without reading input")
	assert.True(t, choice.Hooks, "preset default is Hooks=true; caller overrides later")
	assert.NotContains(t, out.String(), "Install Claude Code hooks",
		"hooks prompt must be suppressed when hooksPreset=true")
}

func TestIsNo(t *testing.T) {
	// Blank input is yes (default pick). Anything starting with n is no.
	// Case and trailing whitespace must not matter.
	assert.False(t, isNo(""))
	assert.False(t, isNo("\n"))
	assert.False(t, isNo("y\n"))
	assert.False(t, isNo("Y\n"))
	assert.False(t, isNo("yes\n"))
	assert.True(t, isNo("n\n"))
	assert.True(t, isNo("N\n"))
	assert.True(t, isNo("no\n"))
	assert.True(t, isNo("  no  \n"))
}
