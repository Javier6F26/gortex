package agents

import "testing"

// gortexGroupCount counts the gortex-authored hook groups under one
// event in a settings root.
func gortexGroupCount(root map[string]any, event string) int {
	hooks, _ := root["hooks"].(map[string]any)
	arr, _ := hooks[event].([]any)
	n := 0
	for _, e := range arr {
		if hookGroupIsGortex(e) {
			n++
		}
	}
	return n
}

func TestUpsertGeminiHooks_InstallsSessionStartAndAfterTool(t *testing.T) {
	root := map[string]any{}
	if !UpsertGeminiHooks(root, "gemini", ApplyOpts{}) {
		t.Fatal("expected the first install to report a change")
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks key not written")
	}
	for _, event := range []string{"SessionStart", "AfterTool"} {
		arr, ok := hooks[event].([]any)
		if !ok || len(arr) != 1 {
			t.Fatalf("%s: want one group, got %v", event, hooks[event])
		}
		group := arr[0].(map[string]any)
		inner := group["hooks"].([]any)[0].(map[string]any)
		if inner["command"] != "gortex hook --agent gemini" {
			t.Errorf("%s command=%v", event, inner["command"])
		}
		if inner["timeout"] != geminiHookTimeoutMS {
			t.Errorf("%s timeout=%v want %d (milliseconds)", event, inner["timeout"], geminiHookTimeoutMS)
		}
	}
	// AfterTool carries a tool matcher; SessionStart does not.
	if _, ok := hooks["AfterTool"].([]any)[0].(map[string]any)["matcher"]; !ok {
		t.Error("AfterTool group should carry a matcher")
	}
	if _, ok := hooks["SessionStart"].([]any)[0].(map[string]any)["matcher"]; ok {
		t.Error("SessionStart group should not carry a matcher")
	}
}

func TestUpsertGeminiHooks_Idempotent(t *testing.T) {
	root := map[string]any{}
	UpsertGeminiHooks(root, "gemini", ApplyOpts{})
	if UpsertGeminiHooks(root, "gemini", ApplyOpts{}) {
		t.Error("a second install without Force should be a no-op")
	}
	if got := gortexGroupCount(root, "SessionStart"); got != 1 {
		t.Errorf("SessionStart gortex groups=%d want 1 (no duplicate)", got)
	}
}

func TestUpsertGeminiHooks_PreservesExistingUserHooks(t *testing.T) {
	root := map[string]any{
		"hooks": map[string]any{
			"AfterTool": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "my-own-linter", "name": "user"}}},
			},
		},
	}
	UpsertGeminiHooks(root, "gemini", ApplyOpts{})
	arr := root["hooks"].(map[string]any)["AfterTool"].([]any)
	if len(arr) != 2 {
		t.Fatalf("expected the user's hook to be preserved alongside gortex's, got %d groups", len(arr))
	}
	// The user's entry must survive untouched.
	first := arr[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if first["command"] != "my-own-linter" {
		t.Errorf("user hook clobbered: %v", first)
	}
}

func TestUpsertGeminiHooks_ForceReplacesGortexOnly(t *testing.T) {
	root := map[string]any{}
	UpsertGeminiHooks(root, "gemini", ApplyOpts{})
	// Add a user hook to AfterTool, then force-reinstall.
	hooks := root["hooks"].(map[string]any)
	hooks["AfterTool"] = append(hooks["AfterTool"].([]any),
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "user-linter", "name": "user"}}})

	if !UpsertGeminiHooks(root, "gemini", ApplyOpts{Force: true}) {
		t.Fatal("Force reinstall should report a change")
	}
	if got := gortexGroupCount(root, "AfterTool"); got != 1 {
		t.Errorf("Force should leave exactly one gortex group, got %d", got)
	}
	// The user's hook still survives.
	arr := hooks["AfterTool"].([]any)
	foundUser := false
	for _, e := range arr {
		inner := e.(map[string]any)["hooks"].([]any)[0].(map[string]any)
		if inner["command"] == "user-linter" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Error("Force must not remove the user's own hook")
	}
}
