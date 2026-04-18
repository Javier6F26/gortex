package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// interactiveChoice is the outcome of the `gortex init` wizard. Now
// that global vs per-repo is owned by the separate `gortex install`
// command, the only remaining interactive knob is hooks — everything
// else has sensible defaults and non-interactive callers (CI,
// --yes, non-TTY stdin) skip the wizard entirely.
type interactiveChoice struct {
	Hooks bool
}

// runInteractiveInit prompts the user for the few decisions `gortex
// init` still needs input for. Today that's just the hooks toggle.
// Separated from the caller so the prompt body stays unit-testable
// with a plain io.Reader.
//
// Returns the decided choice and whether the prompt completed
// successfully. A return of (_, false) means the user terminated the
// prompt early (EOF / Ctrl-D); the caller should fall back to
// historical defaults.
//
// hooksPreset tells the wizard that --hooks / --no-hooks was already
// passed on the command line; in that case we skip the prompt to
// avoid nagging the user about a decision they already made.
func runInteractiveInit(in io.Reader, out io.Writer, hooksPreset bool) (interactiveChoice, bool) {
	reader := bufio.NewReader(in)
	choice := interactiveChoice{Hooks: true}

	if hooksPreset {
		return choice, true
	}

	_, _ = fmt.Fprint(out, "Install Claude Code hooks (PreToolUse + PreCompact + Stop)? [Y/n]: ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return interactiveChoice{}, false
	}
	choice.Hooks = !isNo(line)
	_, _ = fmt.Fprintln(out)
	return choice, true
}

// isNo returns true when the user answered "no" to a yes/no prompt.
// Blank input is yes (the capital Y in "[Y/n]" sets the default).
func isNo(line string) bool {
	s := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(s, "n")
}

// isInteractive reports whether stdin is a terminal — the gate that
// separates "user typed gortex init at a prompt" from "CI script ran
// gortex init." We only prompt in the former case.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
