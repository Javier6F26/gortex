package continuedev

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zzet/gortex/internal/agents"
	"github.com/zzet/gortex/internal/agents/agentstest"
)

func TestContinueCreatesAndIsIdempotent(t *testing.T) {
	env, _ := agentstest.NewEnv(t)
	if err := os.MkdirAll(filepath.Join(env.Root, ".continue"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := New()

	res, err := a.Apply(env, agents.ApplyOpts{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Two creates: the MCP server JSON plus .continue/rules/gortex.md,
	// the per-rule file Continue reads on every chat turn.
	agentstest.AssertCountsByAction(t, res, map[agents.ActionKind]int{agents.ActionCreate: 2})
	agentstest.AssertIdempotent(t, a, env)
}
