package hooks

import (
	"strings"
	"testing"
	"time"
)

func TestRunCodexMalformedJSONNoop(t *testing.T) {
	out := captureStdout(t, func() { runCodex([]byte(`{`), 0) })
	if out != "" {
		t.Fatalf("malformed JSON should be silent, got %q", out)
	}
}

func TestRunCodexIgnoresNonPreToolUse(t *testing.T) {
	data := []byte(`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"rg Foo"}}`)
	out := captureStdout(t, func() { runCodex(data, 0) })
	if out != "" {
		t.Fatalf("non-PreToolUse event should be silent, got %q", out)
	}
}

func TestRunCodexIgnoresNonBash(t *testing.T) {
	data := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"internal/x.go"}}`)
	out := captureStdout(t, func() { runCodex(data, 0) })
	if out != "" {
		t.Fatalf("non-Bash PreToolUse should be silent, got %q", out)
	}
}

func TestRunCodexPreToolUseBashSoftAdditionalContext(t *testing.T) {
	oldProbe := grepProbe
	grepProbe = func(string, time.Duration) ([]grepSymbolHit, error) {
		return nil, errDaemonUnreachable
	}
	t.Cleanup(func() { grepProbe = oldProbe })

	data := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"codex-1","tool_input":{"command":"rg Foo"}}`)
	out := captureStdout(t, func() {
		withStdin(t, data, func() { RunCodex(0) })
	})
	if out == "" {
		t.Fatal("expected Codex Bash PreToolUse guidance, got empty output")
	}
	dec := decodeHookOutput(t, out)
	if dec.HookSpecificOutput == nil {
		t.Fatalf("missing hookSpecificOutput: %s", out)
	}
	hso := dec.HookSpecificOutput
	if hso.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName=%q want PreToolUse", hso.HookEventName)
	}
	if !strings.Contains(hso.AdditionalContext, "PREFER graph tools over Grep") {
		t.Fatalf("additionalContext missing graph guidance: %q", hso.AdditionalContext)
	}
	if hso.PermissionDecision != "" || hso.PermissionDecisionReason != "" {
		t.Fatalf("Codex soft nudge must not deny: %#v", hso)
	}
}
