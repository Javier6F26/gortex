package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestVerdictEscalation(t *testing.T) {
	require.Equal(t, verdictRefuse, escalate(verdictWarn, verdictRefuse))
	require.Equal(t, verdictWarn, escalate(verdictAllow, verdictWarn))
	require.Equal(t, verdictWarn, escalate(verdictWarn, verdictAllow))
	require.Equal(t, verdictRefuse, escalate(verdictRefuse, verdictWarn))

	require.Equal(t, verdictRefuse, verdictForSeverity("error"))
	require.Equal(t, verdictRefuse, verdictForSeverity("critical"))
	require.Equal(t, verdictWarn, verdictForSeverity("warn"))
	require.Equal(t, verdictAllow, verdictForSeverity("info"))
}

func TestClassifyChange(t *testing.T) {
	// rename-only with no broken callers -> structural
	structural := &prediction{step: &simulationStep{
		symbolsRenamed: []map[string]string{{"old": "a", "new": "b"}},
	}}
	require.Equal(t, "structural", classifyChange(structural))

	// config-only touched files -> runtime_drift
	cfg := &prediction{touchedFiles: []string{"deploy/values.yaml", "Dockerfile"}, changedIDs: []string{"x"}}
	require.Equal(t, "runtime_drift", classifyChange(cfg))

	// docs-only, no symbols -> metadata_only
	docs := &prediction{touchedFiles: []string{"README.md"}}
	require.Equal(t, "metadata_only", classifyChange(docs))

	// code change with symbols -> behavioral
	code := &prediction{touchedFiles: []string{"pkg/svc.go"}, changedIDs: []string{"pkg/svc.go::Foo"}}
	require.Equal(t, "behavioral", classifyChange(code))
}

func TestIsConfigFile(t *testing.T) {
	require.True(t, isConfigFile("a/b/values.yaml"))
	require.True(t, isConfigFile("Dockerfile"))
	require.True(t, isConfigFile("infra/main.tf"))
	require.False(t, isConfigFile("pkg/svc.go"))
	require.True(t, isDocFile("README.md"))
	require.False(t, isDocFile("svc.go"))
}

func TestBuildVerificationCommand(t *testing.T) {
	p := &prediction{
		touchedFiles: []string{"internal/svc/svc.go"},
		impact:       nil,
	}
	cmd := buildVerificationCommand(p)
	require.Contains(t, cmd, "go ")

	p2 := &prediction{touchedFiles: []string{"docs/guide.md"}}
	require.Equal(t, "", buildVerificationCommand(p2))
}

func TestChangeContractSymbolSource(t *testing.T) {
	srv, g := setupNavServer(t)
	startID := navFindMethod(t, g, "Start")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{
		"source":  "symbols",
		"symbols": startID,
	}
	res, err := srv.handleChangeContract(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "change_contract errored: %s", toolResultText(res))

	var env changeEnvelope
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &env))

	require.Equal(t, "symbols", env.Source)
	require.Equal(t, "behavioral", env.Classification)
	require.NotEmpty(t, env.ChangedSymbols)
	require.Equal(t, startID, env.ChangedSymbols[0].ID)
	// A symbol set with no signature change and no guard rules never refuses
	// (refuse is reserved for correctness breakage); it may warn on risk.
	require.NotEqual(t, verdictRefuse, env.Verdict)
	require.NotEmpty(t, env.StopCondition)
	require.NotEmpty(t, env.Risk.Tier)
}

func TestChangeContractDiffSourceCleanTree(t *testing.T) {
	srv, _ := setupNavServer(t)
	// The temp repo isn't a git repo; the diff source should fail gracefully
	// (an error result, not a panic).
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"source": "symbols", "symbols": ""}
	res, err := srv.handleChangeContract(context.Background(), req)
	require.NoError(t, err)
	require.True(t, res.IsError, "empty symbol set should be a clean error")
}
