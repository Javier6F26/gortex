package hooks

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/daemon"
)

func TestNormalizeExternalEvent(t *testing.T) {
	cases := map[string]string{
		"SessionStart": "sessionstart",
		"sessionstart": "sessionstart",
		"AfterTool":    "aftertool",
		"PostToolUse":  "aftertool",
		"BeforeTool":   "",
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeExternalEvent(in); got != want {
			t.Errorf("normalizeExternalEvent(%q)=%q want %q", in, got, want)
		}
	}
}

func TestHandleExternalAgent_SessionStartEmitsOrientation(t *testing.T) {
	withFakeStatus(t, func() (*daemon.StatusResponse, error) {
		return &daemon.StatusResponse{
			Version: "1.0.0", Ready: true,
			TrackedRepos: []daemon.TrackedRepoStatus{{Name: "repo", Path: "/tmp/repo", Workspace: "repo", Nodes: 10}},
			Workspaces:   []daemon.WorkspaceSummary{{Slug: "repo"}},
		}, nil
	})
	out := captureStdout(t, func() {
		handleExternalAgent([]byte(`{"hook_event_name":"SessionStart","cwd":"/tmp/repo"}`))
	})
	var payload HookOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid HookOutput JSON: %v\n%s", err, out)
	}
	if payload.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName=%q", payload.HookSpecificOutput.HookEventName)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "Gortex Session Orientation") {
		t.Errorf("expected the orientation block, got:\n%s", payload.HookSpecificOutput.AdditionalContext)
	}
}

func TestHandleExternalAgent_AfterToolDaemonDown(t *testing.T) {
	withFakeStatus(t, func() (*daemon.StatusResponse, error) {
		return nil, errDaemonUnreachable
	})
	out := captureStdout(t, func() {
		handleExternalAgent([]byte(`{"hook_event_name":"AfterTool","tool_name":"run_shell_command","cwd":"/tmp/repo"}`))
	})
	var payload HookOutput
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !strings.Contains(payload.HookSpecificOutput.AdditionalContext, "daemon is not running") {
		t.Errorf("expected a daemon-down hint, got:\n%s", payload.HookSpecificOutput.AdditionalContext)
	}
}

func TestHandleExternalAgent_AfterToolQuietWhenReadyAndCovered(t *testing.T) {
	withFakeStatus(t, func() (*daemon.StatusResponse, error) {
		return &daemon.StatusResponse{
			Ready:        true,
			TrackedRepos: []daemon.TrackedRepoStatus{{Name: "repo", Path: "/tmp/repo"}},
		}, nil
	})
	out := captureStdout(t, func() {
		handleExternalAgent([]byte(`{"hook_event_name":"AfterTool","cwd":"/tmp/repo"}`))
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("AfterTool should stay quiet when the graph is fresh and the cwd is covered, got:\n%s", out)
	}
}

func TestHandleExternalAgent_AfterToolHintsUncoveredCwd(t *testing.T) {
	withFakeStatus(t, func() (*daemon.StatusResponse, error) {
		return &daemon.StatusResponse{
			Ready:        true,
			TrackedRepos: []daemon.TrackedRepoStatus{{Name: "other", Path: "/some/other/repo"}},
		}, nil
	})
	out := captureStdout(t, func() {
		handleExternalAgent([]byte(`{"hook_event_name":"AfterTool","cwd":"/tmp/untracked"}`))
	})
	if !strings.Contains(out, "is not tracked") {
		t.Errorf("expected an untracked-cwd hint, got:\n%s", out)
	}
}

func TestHandleExternalAgent_UnknownEventNoOutput(t *testing.T) {
	out := captureStdout(t, func() {
		handleExternalAgent([]byte(`{"hook_event_name":"BeforeTool"}`))
	})
	if out != "" {
		t.Errorf("unknown event should be a no-op, got: %q", out)
	}
}
