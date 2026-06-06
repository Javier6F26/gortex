package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/agents"
)

// updateAgentRender regenerates the committed agent-render goldens.
// Run: go test ./cmd/gortex -run TestAgentsRenderGolden -update-agent-render
var updateAgentRender = flag.Bool("update-agent-render", false, "regenerate the agent-render drift-fence goldens")

const agentRenderDir = "testdata/agent-render"

// TestAgentsRenderGolden is the all-platform skill-render drift fence:
// it renders every registered adapter and byte-compares the result
// against a committed golden. Any change to an adapter's generated MCP
// config, instructions, hooks, or routing blocks must be accompanied by
// a regenerated golden, so cross-platform drift is reviewable in the
// diff — not just for the Claude bundle.
func TestAgentsRenderGolden(t *testing.T) {
	manifests, err := agents.RenderManifest(buildRegistry().All())
	if err != nil {
		t.Fatalf("render adapters: %v", err)
	}

	if *updateAgentRender {
		if err := os.MkdirAll(agentRenderDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, got := range manifests {
			if err := os.WriteFile(filepath.Join(agentRenderDir, name+".txt"), []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("regenerated %d agent-render goldens", len(manifests))
		return
	}

	for name, got := range manifests {
		golden := filepath.Join(agentRenderDir, name+".txt")
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Errorf("%s: missing golden %s — regenerate with `go test ./cmd/gortex -run TestAgentsRenderGolden -update-agent-render`", name, golden)
			continue
		}
		if string(want) != got {
			t.Errorf("%s: rendered output drifted from %s.\nReview the change; if intended, regenerate goldens:\n  go test ./cmd/gortex -run TestAgentsRenderGolden -update-agent-render", name, golden)
		}
	}

	// A golden with no corresponding adapter means an adapter was
	// removed without dropping its golden — flag it.
	entries, err := os.ReadDir(agentRenderDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".txt")
		if name == e.Name() {
			continue // not a .txt golden
		}
		if _, ok := manifests[name]; !ok {
			t.Errorf("stale golden %s has no matching adapter — delete it or restore the adapter", e.Name())
		}
	}
}
