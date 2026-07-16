package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestLoadRegistryFromGraphAll_FieldParity commits contracts to a graph
// store exactly the way the indexer does (commitInlinedContractToGraph),
// then rehydrates a registry from the persisted kind=contract nodes and
// asserts the reloaded contracts carry the same load-bearing fields as
// the originals. This is the writer-vs-follower parity check: the
// follower's store-backed registry must match what the writer put in.
func TestLoadRegistryFromGraphAll_FieldParity(t *testing.T) {
	g := graph.New()

	want := []Contract{
		{
			ID:         "http::GET::/v1/users/{p1}",
			Type:       ContractHTTP,
			Role:       RoleProvider,
			SymbolID:   "repoA/api/users.go::ListUsers",
			FilePath:   "repoA/api/users.go",
			Line:       42,
			RepoPrefix: "repoA",
			Confidence: 0.9,
			Meta:       map[string]any{"path": "/v1/users/{p1}", "method": "GET"},
		},
		{
			ID:          "grpc::UserService/Get",
			Type:        ContractGRPC,
			Role:        RoleConsumer,
			SymbolID:    "repoB/client/user.go::getUser",
			FilePath:    "repoB/client/user.go",
			Line:        7,
			RepoPrefix:  "repoB",
			WorkspaceID: "team-b",
			ProjectID:   "svc-user",
			Confidence:  0.75,
			Meta:        map[string]any{"service": "UserService"},
		},
	}

	// Writer side: register the contracts and persist each as a node the
	// same way the indexer / wrapper-inline path does.
	writer := NewRegistry()
	for _, c := range want {
		writer.Add(c)
		commitInlinedContractToGraph(g, c)
	}

	// Follower side: rehydrate from the persisted nodes.
	got := LoadRegistryFromGraphAll(g)
	if got == nil {
		t.Fatal("LoadRegistryFromGraphAll returned nil for a store with contracts")
	}
	if len(got.All()) != len(want) {
		t.Fatalf("reloaded %d contracts, want %d", len(got.All()), len(want))
	}

	byID := map[string]Contract{}
	for _, c := range got.All() {
		byID[c.ID] = c
	}
	for _, w := range want {
		g, ok := byID[w.ID]
		if !ok {
			t.Errorf("contract %q missing after reload", w.ID)
			continue
		}
		if g.Type != w.Type {
			t.Errorf("%s: Type = %q, want %q", w.ID, g.Type, w.Type)
		}
		if g.Role != w.Role {
			t.Errorf("%s: Role = %q, want %q", w.ID, g.Role, w.Role)
		}
		if g.SymbolID != w.SymbolID {
			t.Errorf("%s: SymbolID = %q, want %q", w.ID, g.SymbolID, w.SymbolID)
		}
		if g.FilePath != w.FilePath {
			t.Errorf("%s: FilePath = %q, want %q", w.ID, g.FilePath, w.FilePath)
		}
		if g.Line != w.Line {
			t.Errorf("%s: Line = %d, want %d", w.ID, g.Line, w.Line)
		}
		if g.RepoPrefix != w.RepoPrefix {
			t.Errorf("%s: RepoPrefix = %q, want %q", w.ID, g.RepoPrefix, w.RepoPrefix)
		}
		if g.Confidence != w.Confidence {
			t.Errorf("%s: Confidence = %v, want %v", w.ID, g.Confidence, w.Confidence)
		}
		// The node stores EffectiveWorkspace/EffectiveProject (RepoPrefix
		// default when the raw field is empty), so parity is on the
		// effective slug — the value the matcher actually keys on.
		if g.EffectiveWorkspace() != w.EffectiveWorkspace() {
			t.Errorf("%s: EffectiveWorkspace = %q, want %q", w.ID, g.EffectiveWorkspace(), w.EffectiveWorkspace())
		}
		if g.EffectiveProject() != w.EffectiveProject() {
			t.Errorf("%s: EffectiveProject = %q, want %q", w.ID, g.EffectiveProject(), w.EffectiveProject())
		}
		for k, v := range w.Meta {
			if g.Meta[k] != v {
				t.Errorf("%s: Meta[%q] = %v, want %v", w.ID, k, g.Meta[k], v)
			}
		}
	}
}

// TestLoadRegistryFromGraphAll_EmptyStore verifies an empty store yields a
// nil registry so callers can tell "no contracts indexed" from a built
// registry that happens to be empty.
func TestLoadRegistryFromGraphAll_EmptyStore(t *testing.T) {
	if got := LoadRegistryFromGraphAll(graph.New()); got != nil {
		t.Fatalf("expected nil registry for empty store, got %d contracts", len(got.All()))
	}
	if got := LoadRegistryFromGraphAll(nil); got != nil {
		t.Fatal("expected nil registry for nil store")
	}
}
