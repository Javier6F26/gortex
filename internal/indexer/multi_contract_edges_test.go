package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/query"
	"github.com/zzet/gortex/internal/search"
)

// newMultiLangRegistry registers Go + TypeScript for tests that exercise
// cross-language contracts (e.g. TS consumer → Go provider).
func newMultiLangRegistry() *parser.Registry {
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	return reg
}

// setupHTTPProviderRepo writes a Go file declaring a Gin route that binds
// GET /api/users to a handler function. After indexing, HTTPExtractor
// produces a provider contract with SymbolID pointing at the enclosing
// function (setupRoutes).
func setupHTTPProviderRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/"+name+"\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "github.com/gin-gonic/gin"

func setupRoutes(r *gin.Engine) {
	r.GET("/api/users", listUsers)
}

func listUsers() {}
`), 0o644))
	return dir
}

// setupHTTPConsumerRepo writes a Go file with an http.Get call to the same
// path. HTTPExtractor produces a consumer contract with SymbolID pointing
// at fetchUsers. After ReconcileContractEdges, fetchUsers --matches-->
// setupRoutes should exist in the graph.
func setupHTTPConsumerRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/"+name+"\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.go"), []byte(`package main

import "net/http"

func fetchUsers() {
	http.Get("http://api.example.com/api/users")
}
`), 0o644))
	return dir
}

// TestReconcileContractEdges_BridgesConsumerToProvider is the north-star
// test for cross-service request tracing. After indexing a provider and a
// consumer in two separate tracked repos, get_call_chain from the consumer
// function must traverse into the provider's handler region. Without the
// matcher's output persisted as EdgeMatches, the BFS stops at the
// consumer-side HTTP call — nothing bridges the service boundary.
func TestReconcileContractEdges_BridgesConsumerToProvider(t *testing.T) {
	providerRoot := setupHTTPProviderRepo(t, "provider-svc")
	consumerRoot := setupHTTPConsumerRepo(t, "consumer-svc")

	tmpCfg := filepath.Join(t.TempDir(), "config.yaml")
	gc := &config.GlobalConfig{
		Repos: []config.RepoEntry{
			{Path: providerRoot, Name: "provider-svc"},
			{Path: consumerRoot, Name: "consumer-svc"},
		},
	}
	gc.SetConfigPath(tmpCfg)
	require.NoError(t, gc.Save())

	cm, err := config.NewConfigManager(tmpCfg)
	require.NoError(t, err)

	g := graph.New()
	mi := NewMultiIndexer(g, newTestRegistry(), search.NewBM25(), cm, zap.NewNop())

	for _, entry := range cm.Global().Repos {
		_, err := mi.TrackRepoCtx(context.Background(), entry)
		require.NoError(t, err, "track %s", entry.Name)
	}

	// EdgeMatches must land on the handler function (listUsers), not on
	// the registration helper (setupRoutes). The HTTP provider extractor
	// captures the handler identifier from `r.GET("/path", handler)`
	// patterns — T1.3 — so "trace a request" lands on business logic.
	consumerSym := "consumer-svc/client.go::fetchUsers"
	providerSym := "provider-svc/main.go::listUsers"

	var matchEdge *graph.Edge
	for _, e := range g.AllEdges() {
		if e.Kind != graph.EdgeMatches {
			continue
		}
		if e.From == consumerSym && e.To == providerSym {
			matchEdge = e
			break
		}
	}
	require.NotNil(t, matchEdge,
		"expected EdgeMatches %s → %s; present match edges were: %v",
		consumerSym, providerSym, collectMatchEdges(g))
	assert.True(t, matchEdge.CrossRepo,
		"consumer and provider live in different repos — CrossRepo flag must be set")

	// Positive end-to-end: walking forward from the consumer symbol reaches
	// the provider symbol. This is what "trace a request through product"
	// relies on.
	eng := query.NewEngine(g)
	chain := eng.GetCallChain(consumerSym, query.QueryOptions{Depth: 4, Limit: 50, Detail: "brief"})
	require.NotNil(t, chain)
	reached := false
	for _, n := range chain.Nodes {
		if n.ID == providerSym {
			reached = true
			break
		}
	}
	assert.True(t, reached,
		"get_call_chain(%s) did not reach %s; chain nodes: %v",
		consumerSym, providerSym, nodeIDs(chain.Nodes))
}

// setupTSConsumerRepo writes a TypeScript file that builds its request URL
// from a template literal (${API_URL}/path) — the dominant pattern in the
// web/extension/mobile consumers in the tuck audit. T1.1 normalization
// must strip the base-URL placeholder so the consumer contract ID matches
// the provider's /v1/users.
func setupTSConsumerRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"`+name+`","version":"0.0.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "client.ts"), []byte(
		"const API_URL = \"https://api.example.com\";\n"+
			"export async function fetchUsers() {\n"+
			"  return fetch(`${API_URL}/api/users`);\n"+
			"}\n",
	), 0o644))
	return dir
}

// TestReconcileContractEdges_TemplateLiteralConsumer is T1.1's cross-repo
// integration guard. A TypeScript consumer constructs the request URL
// from a template literal whose base is an interpolated constant; the Go
// provider declares "/api/users" verbatim. Without NormalizeHTTPPath's
// template-literal stripping, the consumer's contract ID carries the
// placeholder and never matches the provider, so no EdgeMatches forms
// and get_call_chain stops at the fetch call site.
func TestReconcileContractEdges_TemplateLiteralConsumer(t *testing.T) {
	providerRoot := setupHTTPProviderRepo(t, "provider-svc")
	consumerRoot := setupTSConsumerRepo(t, "web-ui")

	tmpCfg := filepath.Join(t.TempDir(), "config.yaml")
	gc := &config.GlobalConfig{
		Repos: []config.RepoEntry{
			{Path: providerRoot, Name: "provider-svc"},
			{Path: consumerRoot, Name: "web-ui"},
		},
	}
	gc.SetConfigPath(tmpCfg)
	require.NoError(t, gc.Save())

	cm, err := config.NewConfigManager(tmpCfg)
	require.NoError(t, err)

	g := graph.New()
	mi := NewMultiIndexer(g, newMultiLangRegistry(), search.NewBM25(), cm, zap.NewNop())
	for _, entry := range cm.Global().Repos {
		_, err := mi.TrackRepoCtx(context.Background(), entry)
		require.NoError(t, err, "track %s", entry.Name)
	}

	consumerSym := "web-ui/src/client.ts::fetchUsers"
	providerSym := "provider-svc/main.go::listUsers"

	var matchEdge *graph.Edge
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeMatches && e.From == consumerSym && e.To == providerSym {
			matchEdge = e
			break
		}
	}
	require.NotNil(t, matchEdge,
		"expected EdgeMatches %s → %s after template-literal normalization; present match edges were: %v",
		consumerSym, providerSym, collectMatchEdges(g))
	assert.True(t, matchEdge.CrossRepo, "consumer and provider live in different repos")

	eng := query.NewEngine(g)
	chain := eng.GetCallChain(consumerSym, query.QueryOptions{Depth: 4, Limit: 50, Detail: "brief"})
	reached := false
	for _, n := range chain.Nodes {
		if n.ID == providerSym {
			reached = true
			break
		}
	}
	assert.True(t, reached,
		"get_call_chain(%s) must reach %s across service boundary; chain was: %v",
		consumerSym, providerSym, nodeIDs(chain.Nodes))
}

// setupDartConsumerRepo writes a Flutter-shape api-client file with dio
// calls to clean absolute paths and Dart's bare-$id interpolation for the
// path param — the pattern tuck_app's TuckApiClient uses. T2.1 recognizes
// these as consumer contracts; NormalizeHTTPPath collapses $id to {id}.
func setupDartConsumerRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lib", "core"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pubspec.yaml"),
		[]byte("name: "+name+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib", "core", "api_client.dart"), []byte(
		"class TuckApiClient {\n"+
			"  final Dio _dio;\n"+
			"  TuckApiClient(this._dio);\n"+
			"\n"+
			"  Future<void> fetchUsers() async {\n"+
			"    await _dio.get('/api/users');\n"+
			"  }\n"+
			"}\n",
	), 0o644))
	return dir
}

// TestReconcileContractEdges_DartConsumer is the cross-language guard for
// T2.1 — a Flutter app's dio-based API client bridges to the Go provider.
// Without Dart patterns in the extractor the consumer would never produce
// a contract, the matcher would never pair, and get_call_chain would stop
// at TuckApiClient.fetchUsers instead of reaching the provider handler.
func TestReconcileContractEdges_DartConsumer(t *testing.T) {
	providerRoot := setupHTTPProviderRepo(t, "provider-svc")
	consumerRoot := setupDartConsumerRepo(t, "mobile-app")

	tmpCfg := filepath.Join(t.TempDir(), "config.yaml")
	gc := &config.GlobalConfig{
		Repos: []config.RepoEntry{
			{Path: providerRoot, Name: "provider-svc"},
			{Path: consumerRoot, Name: "mobile-app"},
		},
	}
	gc.SetConfigPath(tmpCfg)
	require.NoError(t, gc.Save())

	cm, err := config.NewConfigManager(tmpCfg)
	require.NoError(t, err)

	g := graph.New()
	mi := NewMultiIndexer(g, newMultiLangRegistry(), search.NewBM25(), cm, zap.NewNop())
	for _, entry := range cm.Global().Repos {
		_, err := mi.TrackRepoCtx(context.Background(), entry)
		require.NoError(t, err, "track %s", entry.Name)
	}

	// The Dart extractor names methods by their short name, so the enclosing
	// symbol of the dio.get call is TuckApiClient.fetchUsers — the method.
	// The exact Dart symbol ID format depends on the Dart tree-sitter
	// extractor, so accept any consumer ID in the mobile-app repo whose
	// name ends in "fetchUsers".
	providerSym := "provider-svc/main.go::listUsers"

	var matchEdge *graph.Edge
	for _, e := range g.AllEdges() {
		if e.Kind != graph.EdgeMatches {
			continue
		}
		if e.To != providerSym {
			continue
		}
		n := g.GetNode(e.From)
		if n != nil && n.Name == "fetchUsers" && n.RepoPrefix == "mobile-app" {
			matchEdge = e
			break
		}
	}
	require.NotNil(t, matchEdge,
		"expected EdgeMatches from Dart fetchUsers to %s; present match edges: %v",
		providerSym, collectMatchEdges(g))
	assert.True(t, matchEdge.CrossRepo,
		"consumer (Dart) and provider (Go) live in different repos")

	eng := query.NewEngine(g)
	chain := eng.GetCallChain(matchEdge.From, query.QueryOptions{Depth: 4, Limit: 50, Detail: "brief"})
	reached := false
	for _, n := range chain.Nodes {
		if n.ID == providerSym {
			reached = true
			break
		}
	}
	assert.True(t, reached,
		"get_call_chain(%s) must reach %s; chain was: %v",
		matchEdge.From, providerSym, nodeIDs(chain.Nodes))
}

// setupTSWrapperRepo mirrors tuck's web/lib/api.ts shape: a private
// doFetch that calls fetch(`${API_URL}${path}`), a private request
// wrapper that forwards its path parameter to doFetch, and several
// exported per-endpoint functions that each call request with a
// literal path (some plain, some template-literal with params, some
// carrying a method: in the options). The wrapper chain has depth 2 —
// doFetch is the initial wrapper, request is discovered in the
// second BFS pass, and the exported functions are where inline
// contracts finally land.
func setupTSWrapperRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"`+name+`","version":"0.0.0"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib", "api.ts"), []byte(
		"const API_URL = \"https://api.example.com\";\n"+
			"\n"+
			"async function doFetch(path: string, token: string, options: any = {}) {\n"+
			"  return fetch(`${API_URL}${path}`, { ...options, headers: { Authorization: `Bearer ${token}` } });\n"+
			"}\n"+
			"\n"+
			"async function request<T>(path: string, getToken: () => Promise<string>, options: any = {}): Promise<T> {\n"+
			"  const token = await getToken();\n"+
			"  const res = await doFetch(path, token, options);\n"+
			"  return res.json();\n"+
			"}\n"+
			"\n"+
			"export async function fetchUsers(getToken: () => Promise<string>) {\n"+
			"  return request<any>('/api/users', getToken);\n"+
			"}\n"+
			"\n"+
			"export async function fetchUser(getToken: () => Promise<string>, id: string) {\n"+
			"  return request<any>(`/api/users/${id}`, getToken);\n"+
			"}\n"+
			"\n"+
			"export async function createUser(getToken: () => Promise<string>, data: any) {\n"+
			"  return request<any>('/api/users', getToken, { method: 'POST', body: JSON.stringify(data) });\n"+
			"}\n"+
			"\n"+
			"export async function deleteUser(getToken: () => Promise<string>, id: string) {\n"+
			"  return request<void>(`/api/users/${id}`, getToken, { method: 'DELETE' });\n"+
			"}\n",
	), 0o644))
	return dir
}

// setupGoProviderRepo writes a handler for every endpoint the TS
// wrapper repo calls. listUsers, getUser, createUser, deleteUser.
// Gin-style route declarations so T1.3 handler resolution can pick
// the method-level handler as the match target.
func setupGoProviderRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/"+name+"\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "github.com/gin-gonic/gin"

func setupRoutes(r *gin.Engine) {
	r.GET("/api/users", listUsers)
	r.GET("/api/users/:id", getUser)
	r.POST("/api/users", createUser)
	r.DELETE("/api/users/:id", deleteUser)
}

func listUsers()   {}
func getUser()     {}
func createUser()  {}
func deleteUser()  {}
`), 0o644))
	return dir
}

// TestInlineWrappers_TuckShape is the T2.4 north-star test. A TS
// wrapper chain (doFetch → request → exported fetch/create/delete
// functions) plus a Go provider with matching routes must produce
// one EdgeMatches per endpoint, not one meta-match behind an
// unresolvable wrapper contract.
//
// The test asserts three things that together define the feature
// working end-to-end:
//
//  1. A match edge exists from each exported TS function (fetchUsers,
//     fetchUser, createUser, deleteUser) to the corresponding Go
//     handler — proving wrapper inlining emits per-caller contracts
//     and that method inference distinguishes GET from POST/DELETE.
//  2. The edges have CrossRepo set, since consumer and provider live
//     in different repos.
//  3. get_call_chain from any exported function reaches its matched
//     handler across the bridge.
func TestInlineWrappers_TuckShape(t *testing.T) {
	providerRoot := setupGoProviderRepo(t, "provider-svc")
	consumerRoot := setupTSWrapperRepo(t, "web-ui")

	tmpCfg := filepath.Join(t.TempDir(), "config.yaml")
	gc := &config.GlobalConfig{
		Repos: []config.RepoEntry{
			{Path: providerRoot, Name: "provider-svc"},
			{Path: consumerRoot, Name: "web-ui"},
		},
	}
	gc.SetConfigPath(tmpCfg)
	require.NoError(t, gc.Save())

	cm, err := config.NewConfigManager(tmpCfg)
	require.NoError(t, err)

	g := graph.New()
	mi := NewMultiIndexer(g, newMultiLangRegistry(), search.NewBM25(), cm, zap.NewNop())
	for _, entry := range cm.Global().Repos {
		_, err := mi.TrackRepoCtx(context.Background(), entry)
		require.NoError(t, err, "track %s", entry.Name)
	}

	type bridge struct{ consumer, provider string }
	wantBridges := []bridge{
		{"web-ui/lib/api.ts::fetchUsers", "provider-svc/main.go::listUsers"},
		{"web-ui/lib/api.ts::fetchUser", "provider-svc/main.go::getUser"},
		{"web-ui/lib/api.ts::createUser", "provider-svc/main.go::createUser"},
		{"web-ui/lib/api.ts::deleteUser", "provider-svc/main.go::deleteUser"},
	}

	have := make(map[string]*graph.Edge)
	for _, e := range g.AllEdges() {
		if e.Kind != graph.EdgeMatches {
			continue
		}
		have[e.From+"|"+e.To] = e
	}

	for _, b := range wantBridges {
		e, ok := have[b.consumer+"|"+b.provider]
		if !ok {
			t.Errorf("missing bridge %s → %s; have: %v", b.consumer, b.provider, matchEdgeSummaries(g))
			continue
		}
		assert.True(t, e.CrossRepo,
			"bridge %s → %s: CrossRepo must be set for TS→Go chain", b.consumer, b.provider)
	}

	// get_call_chain spot-check — pick the POST case, since method
	// inference is what distinguishes createUser → createUser (POST)
	// from fetchUsers → listUsers (GET).
	eng := query.NewEngine(g)
	chain := eng.GetCallChain("web-ui/lib/api.ts::createUser",
		query.QueryOptions{Depth: 4, Limit: 50, Detail: "brief"})
	reached := false
	for _, n := range chain.Nodes {
		if n.ID == "provider-svc/main.go::createUser" {
			reached = true
			break
		}
	}
	assert.True(t, reached,
		"get_call_chain(createUser[TS]) must reach createUser[Go] handler across the wrapper bridge; chain: %v",
		nodeIDs(chain.Nodes))
}

// matchEdgeSummaries dumps all EdgeMatches as "from → to" strings for
// failure-message context when the expected bridges aren't present.
func matchEdgeSummaries(g *graph.Graph) []string {
	var out []string
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeMatches {
			out = append(out, e.From+" → "+e.To)
		}
	}
	return out
}

// TestReconcileContractEdges_PurgesStaleOnUntrack asserts that removing
// the consumer repo deletes its match edges — otherwise the graph would
// accumulate dangling edges pointing at symbols that no longer exist.
func TestReconcileContractEdges_PurgesStaleOnUntrack(t *testing.T) {
	providerRoot := setupHTTPProviderRepo(t, "provider-svc")
	consumerRoot := setupHTTPConsumerRepo(t, "consumer-svc")

	tmpCfg := filepath.Join(t.TempDir(), "config.yaml")
	gc := &config.GlobalConfig{
		Repos: []config.RepoEntry{
			{Path: providerRoot, Name: "provider-svc"},
			{Path: consumerRoot, Name: "consumer-svc"},
		},
	}
	gc.SetConfigPath(tmpCfg)
	require.NoError(t, gc.Save())

	cm, err := config.NewConfigManager(tmpCfg)
	require.NoError(t, err)

	g := graph.New()
	mi := NewMultiIndexer(g, newTestRegistry(), search.NewBM25(), cm, zap.NewNop())
	for _, entry := range cm.Global().Repos {
		_, err := mi.TrackRepoCtx(context.Background(), entry)
		require.NoError(t, err)
	}

	require.NotEmpty(t, collectMatchEdges(g), "setup precondition: at least one EdgeMatches must exist")

	mi.UntrackRepo("consumer-svc")

	remaining := collectMatchEdges(g)
	assert.Empty(t, remaining,
		"untracking the consumer must purge its match edges; found %d leftover: %v",
		len(remaining), remaining)
}

func collectMatchEdges(g *graph.Graph) []string {
	var out []string
	for _, e := range g.AllEdges() {
		if e.Kind == graph.EdgeMatches {
			out = append(out, e.From+" → "+e.To)
		}
	}
	return out
}

func nodeIDs(nodes []*graph.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}
