package store_pg_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/contracts"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
)

// persistContractNode writes a kind=contract node the same way the indexer's
// commitInlinedContractToGraph does — the full record stamped onto Node.Meta
// so a follower can rehydrate without the gob snapshot. Kept in lockstep with
// internal/contracts/wrapper.go::commitInlinedContractToGraph.
func persistContractNode(g graph.Store, c contracts.Contract) {
	g.AddNode(&graph.Node{
		ID:          c.ID,
		Kind:        graph.KindContract,
		Name:        c.ID,
		FilePath:    c.FilePath,
		Language:    "contract",
		RepoPrefix:  c.RepoPrefix,
		WorkspaceID: c.EffectiveWorkspace(),
		ProjectID:   c.EffectiveProject(),
		Meta: map[string]any{
			"type":          string(c.Type),
			"role":          string(c.Role),
			"symbol_id":     c.SymbolID,
			"line":          c.Line,
			"confidence":    c.Confidence,
			"contract_meta": c.Meta,
		},
	})
}

// TestPGContractHydrationParity is the fix-contracts-hydration-residuals 1.1
// regression, on real PostgreSQL: contract nodes persisted as the indexer
// persists them must rehydrate 1:1 through the store-backed fallback the
// follower uses (LoadRegistryFromGraphAll), with the load-bearing fields
// intact — writer-vs-follower field parity. Also asserts count parity:
// the rehydrated registry totals the store's contract-node count (the
// property `contracts {all_repos:true}` reports on a follower).
func TestPGContractHydrationParity(t *testing.T) {
	skipIfNoPG(t)
	dsn, schema := createTestSchema(t)
	ctx := context.Background()
	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn, Schema: schema})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	want := []contracts.Contract{
		{
			ID: "http::GET::/v1/users/{p1}", Type: contracts.ContractHTTP, Role: contracts.RoleProvider,
			SymbolID: "repoA/api/users.go::ListUsers", FilePath: "repoA/api/users.go", Line: 42,
			RepoPrefix: "repoA", Confidence: 0.9,
			Meta: map[string]any{"path": "/v1/users/{p1}", "method": "GET"},
		},
		{
			ID: "grpc::UserService/Get", Type: contracts.ContractGRPC, Role: contracts.RoleConsumer,
			SymbolID: "repoB/client/user.go::getUser", FilePath: "repoB/client/user.go", Line: 7,
			RepoPrefix: "repoB", WorkspaceID: "team-b", ProjectID: "svc-user", Confidence: 0.75,
			Meta: map[string]any{"service": "UserService"},
		},
		{
			ID: "env::JWT_ALGORITHM", Type: contracts.ContractType("env"), Role: contracts.RoleConsumer,
			FilePath: "repoB/config.go", Line: 6, RepoPrefix: "repoB", Confidence: 0.9,
			Meta: map[string]any{"var": "JWT_ALGORITHM"},
		},
	}
	for _, c := range want {
		persistContractNode(st, c)
	}

	// Count parity: NodesByKind sees exactly what we persisted.
	var nodeCount int
	for n := range st.NodesByKind(graph.KindContract) {
		if n != nil {
			nodeCount++
		}
	}
	require.Equal(t, len(want), nodeCount, "store must hold one contract node per persisted contract")

	// Follower rehydration path.
	reg := contracts.LoadRegistryFromGraphAll(st)
	require.NotNil(t, reg, "LoadRegistryFromGraphAll must materialize a registry for a store with contracts")
	require.Equal(t, nodeCount, len(reg.All()),
		"rehydrated registry must total the store's contract-node count (no entry dropped)")

	byID := map[string]contracts.Contract{}
	for _, c := range reg.All() {
		byID[c.ID] = c
	}
	for _, w := range want {
		g, ok := byID[w.ID]
		require.True(t, ok, "contract %q missing after rehydration", w.ID)
		assert.Equal(t, w.Type, g.Type, "%s Type", w.ID)
		assert.Equal(t, w.Role, g.Role, "%s Role", w.ID)
		assert.Equal(t, w.SymbolID, g.SymbolID, "%s SymbolID", w.ID)
		assert.Equal(t, w.FilePath, g.FilePath, "%s FilePath", w.ID)
		assert.Equal(t, w.Line, g.Line, "%s Line", w.ID)
		assert.Equal(t, w.RepoPrefix, g.RepoPrefix, "%s RepoPrefix", w.ID)
		assert.Equal(t, w.Confidence, g.Confidence, "%s Confidence", w.ID)
		assert.Equal(t, w.EffectiveWorkspace(), g.EffectiveWorkspace(), "%s EffectiveWorkspace", w.ID)
		assert.Equal(t, w.EffectiveProject(), g.EffectiveProject(), "%s EffectiveProject", w.ID)
		for k, v := range w.Meta {
			assert.EqualValues(t, v, g.Meta[k], "%s Meta[%q]", w.ID, k)
		}
	}
}
