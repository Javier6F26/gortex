package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
)

// runContractOnBar evaluates change_contract over Bar (consumer.go), which
// calls Foo (api.go), under the given guard rules, and returns the envelope.
func runContractOnBar(t *testing.T, rules []config.GuardRule) changeEnvelope {
	t.Helper()
	srv, g := setupAPIServer(t)
	srv.guardRules = rules

	var barID string
	for _, n := range g.AllNodes() {
		if n.Name == "Bar" && n.Kind == graph.KindFunction {
			barID = n.ID
		}
	}
	require.NotEmpty(t, barID, "Bar not indexed")

	req := mcplib.CallToolRequest{}
	req.Params.Arguments = map[string]any{"source": "symbols", "symbols": barID}
	res, err := srv.handleChangeContract(context.Background(), req)
	require.NoError(t, err)
	require.False(t, res.IsError, "change_contract errored: %s", toolResultText(res))

	var env changeEnvelope
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(mcplib.TextContent).Text), &env))
	return env
}

func hasFamily(env changeEnvelope, family string) bool {
	for _, r := range env.Reasons {
		if r.Family == family {
			return true
		}
	}
	return false
}

func TestChangeContractSeverityError(t *testing.T) {
	env := runContractOnBar(t, []config.GuardRule{{
		Name: "no-consumer-to-api", Kind: "boundary",
		Source: "consumer.go", Target: "api.go", Severity: "error",
	}})
	require.True(t, hasFamily(env, "architecture"), "boundary rule should fire")
	require.Equal(t, verdictRefuse, env.Verdict, "an error-severity boundary break must refuse")
}

func TestChangeContractSeverityDefaultWarns(t *testing.T) {
	env := runContractOnBar(t, []config.GuardRule{{
		Name: "no-consumer-to-api", Kind: "boundary",
		Source: "consumer.go", Target: "api.go", // no severity -> default warn
	}})
	require.True(t, hasFamily(env, "architecture"), "boundary rule should fire")
	require.NotEqual(t, verdictRefuse, env.Verdict, "a default-severity break warns, never refuses")
}

func TestChangeContractExceptGlobExempts(t *testing.T) {
	env := runContractOnBar(t, []config.GuardRule{{
		Name: "no-consumer-to-api", Kind: "boundary",
		Source: "consumer.go", Target: "api.go", Severity: "error",
		Except: []string{"consumer.go"},
	}})
	require.False(t, hasFamily(env, "architecture"), "except glob should exempt consumer.go")
}
