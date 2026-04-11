package hooks

import (
	"strings"
	"testing"
)

func TestEnrichGlob_SourcePattern(t *testing.T) {
	result := enrichGlob(map[string]any{"pattern": "**/*.go"})
	if result == "" {
		t.Fatal("expected guidance for source glob pattern, got empty")
	}
	if !strings.Contains(result, "search_symbols") {
		t.Error("expected guidance to mention search_symbols")
	}
	if !strings.Contains(result, "PREFER graph tools") {
		t.Error("expected PREFER graph tools header")
	}
}

func TestEnrichGlob_NonSourcePattern(t *testing.T) {
	result := enrichGlob(map[string]any{"pattern": "**/*.json"})
	if result != "" {
		t.Errorf("expected empty for non-source glob, got: %s", result)
	}
}

func TestEnrichGlob_EmptyPattern(t *testing.T) {
	result := enrichGlob(map[string]any{"pattern": ""})
	if result != "" {
		t.Errorf("expected empty for empty pattern, got: %s", result)
	}
}

func TestEnrichRead_Guidance(t *testing.T) {
	// Port 0 means bridge won't respond — should still return guidance.
	result := enrichRead(map[string]any{"file_path": "/tmp/foo.go"}, 0)
	if result == "" {
		t.Fatal("expected guidance for source file read, got empty")
	}
	if !strings.Contains(result, "get_symbol_source") {
		t.Error("expected guidance to mention get_symbol_source")
	}
	if !strings.Contains(result, "get_editing_context") {
		t.Error("expected guidance to mention get_editing_context")
	}
}

func TestEnrichRead_NonSourceFile(t *testing.T) {
	result := enrichRead(map[string]any{"file_path": "/tmp/config.json"}, 0)
	if result != "" {
		t.Errorf("expected empty for non-source file, got: %s", result)
	}
}

func TestEnrichGrep_Guidance(t *testing.T) {
	// Port 0 means bridge won't respond — should still return guidance.
	result := enrichGrep(map[string]any{"pattern": "handleFindUsages"}, 0)
	if result == "" {
		t.Fatal("expected guidance for grep, got empty")
	}
	if !strings.Contains(result, "search_symbols") {
		t.Error("expected guidance to mention search_symbols")
	}
	if !strings.Contains(result, "find_usages") {
		t.Error("expected guidance to mention find_usages")
	}
}

func TestEnrichGrep_ShortPattern(t *testing.T) {
	result := enrichGrep(map[string]any{"pattern": "ab"}, 0)
	if result != "" {
		t.Errorf("expected empty for short pattern, got: %s", result)
	}
}

func TestEnrich_DispatchesCorrectly(t *testing.T) {
	tests := []struct {
		tool     string
		input    map[string]any
		wantNon  bool
	}{
		{"Read", map[string]any{"file_path": "/tmp/foo.go"}, true},
		{"Grep", map[string]any{"pattern": "handleFoo"}, true},
		{"Glob", map[string]any{"pattern": "**/*.ts"}, true},
		{"Glob", map[string]any{"pattern": "**/*.json"}, false},
		{"Write", map[string]any{}, false},
	}
	for _, tt := range tests {
		result := enrich(HookInput{
			HookEventName: "PreToolUse",
			ToolName:      tt.tool,
			ToolInput:     tt.input,
		}, 0)
		if tt.wantNon && result == "" {
			t.Errorf("enrich(%s) returned empty, expected non-empty", tt.tool)
		}
		if !tt.wantNon && result != "" {
			t.Errorf("enrich(%s) returned non-empty, expected empty", tt.tool)
		}
	}
}
