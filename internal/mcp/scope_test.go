package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// These tests pin the wire-shape validation ResolveScopedRepos performs
// per the tool's declared scope. The active workspace boundary itself is
// enforced at the graph level by the daemon's per-session scope
// (sessionScope / nodeInSessionScope); that isolation is exercised by
// scope_integration_test.go, not here.

// --- ScopeRepo --------------------------------------------------------

func TestScopeRepo_RequiresString(t *testing.T) {
	got, errResult := ResolveScopedRepos(ScopeRepo, nil)
	if errResult == nil {
		t.Fatalf("expected protocol error for missing repo, got %+v", got)
	}
	assertErrorContains(t, errResult, `scope \"repo\"`)
	assertErrorContains(t, errResult, "string")
}

func TestScopeRepo_RejectsList(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeRepo, []any{"alpha"})
	if errResult == nil {
		t.Fatal("list-typed repo on scope=repo must be a protocol error")
	}
	assertErrorContains(t, errResult, "list")
}

func TestScopeRepo_AcceptsString(t *testing.T) {
	got, errResult := ResolveScopedRepos(ScopeRepo, "alpha")
	if errResult != nil {
		t.Fatalf("unexpected error: %s", errToString(errResult))
	}
	if got.Kind != ScopeRepo {
		t.Fatalf("kind = %v, want repo", got.Kind)
	}
	if !equalSlice(got.Repos, []string{"alpha"}) {
		t.Fatalf("repos = %v, want [alpha]", got.Repos)
	}
}

func TestScopeRepo_RejectsEmptyString(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeRepo, "")
	if errResult == nil {
		t.Fatal("empty string repo must be rejected")
	}
}

// --- ScopeWorkspace ---------------------------------------------------

func TestScopeWorkspace_RejectsAnyRepo(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeWorkspace, "alpha")
	if errResult == nil {
		t.Fatal("scope=workspace must reject string repo")
	}
	_, errResult = ResolveScopedRepos(ScopeWorkspace, []any{"alpha"})
	if errResult == nil {
		t.Fatal("scope=workspace must reject list repo")
	}
	_, errResult = ResolveScopedRepos(ScopeWorkspace, []any{"*"})
	if errResult == nil {
		t.Fatal("scope=workspace must reject [*] repo")
	}
}

func TestScopeWorkspace_NoRepoOK(t *testing.T) {
	got, errResult := ResolveScopedRepos(ScopeWorkspace, nil)
	if errResult != nil {
		t.Fatalf("unexpected: %s", errToString(errResult))
	}
	if got.Kind != ScopeWorkspace {
		t.Fatalf("kind = %v, want workspace", got.Kind)
	}
}

// --- ScopeFanOut ------------------------------------------------------

func TestScopeFanOut_RequiresList(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeFanOut, nil)
	if errResult == nil {
		t.Fatal("missing repo on scope=fan-out must be a protocol error")
	}
	assertErrorContains(t, errResult, `[\"*\"]`)
}

func TestScopeFanOut_RejectsString(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeFanOut, "alpha")
	if errResult == nil {
		t.Fatal("scope=fan-out must reject single-string repo")
	}
}

func TestScopeFanOut_EmptyListRejected(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeFanOut, []any{})
	if errResult == nil {
		t.Fatal("empty list repo must be a protocol error")
	}
	assertErrorContains(t, errResult, "non-empty")
}

func TestScopeFanOut_NamedSubset(t *testing.T) {
	got, errResult := ResolveScopedRepos(ScopeFanOut, []any{"alpha", "gamma"})
	if errResult != nil {
		t.Fatalf("unexpected: %s", errToString(errResult))
	}
	if !equalSlice(got.Repos, []string{"alpha", "gamma"}) {
		t.Fatalf("repos = %v, want [alpha gamma]", got.Repos)
	}
}

func TestScopeFanOut_StarSentinelMustBeAlone(t *testing.T) {
	_, errResult := ResolveScopedRepos(ScopeFanOut, []any{"*", "alpha"})
	if errResult == nil {
		t.Fatal("[*] cannot mix with named entries")
	}
	assertErrorContains(t, errResult, "mix")
}

func TestScopeFanOut_NamedSubsetDeduplicates(t *testing.T) {
	got, errResult := ResolveScopedRepos(ScopeFanOut, []any{"alpha", "beta", "alpha"})
	if errResult != nil {
		t.Fatalf("unexpected: %s", errToString(errResult))
	}
	if !equalSlice(got.Repos, []string{"alpha", "beta"}) {
		t.Fatalf("dedup expected; got %v", got.Repos)
	}
}

// --- Registry --------------------------------------------------------

// TestEveryDefaultToolHasScopeKind guards against future tool additions
// that forget to declare a scope.
func TestEveryDefaultToolHasScopeKind(t *testing.T) {
	if len(defaultToolScopes) == 0 {
		t.Fatal("defaultToolScopes empty — scope migration incomplete")
	}
	for name, scope := range defaultToolScopes {
		switch scope {
		case ScopeRepo, ScopeWorkspace, ScopeFanOut:
			// ok
		default:
			t.Errorf("tool %q has unrecognised scope %v", name, scope)
		}
	}
}

func TestScopeRegistry_RoundTrip(t *testing.T) {
	r := newScopeRegistry()
	r.set("find_usages", ScopeRepo)
	r.set("list_repos", ScopeWorkspace)
	r.set("audit_agent_config", ScopeFanOut)

	got, ok := r.get("find_usages")
	if !ok || got != ScopeRepo {
		t.Fatalf("get(find_usages) = (%v, %v), want (repo, true)", got, ok)
	}
	if names := r.allTools(); !equalSlice(names, []string{
		"audit_agent_config", "find_usages", "list_repos",
	}) {
		t.Fatalf("allTools sort wrong: %v", names)
	}
	snap := r.snapshot()
	if snap["find_usages"] != "repo" {
		t.Fatalf("snapshot(find_usages) = %q, want repo", snap["find_usages"])
	}
	if snap["list_repos"] != "workspace" {
		t.Fatalf("snapshot(list_repos) = %q, want workspace", snap["list_repos"])
	}
	if snap["audit_agent_config"] != "fan-out" {
		t.Fatalf("snapshot(audit_agent_config) = %q, want fan-out", snap["audit_agent_config"])
	}
}

// --- helpers ---------------------------------------------------------

func errResultBody(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func errToString(res *mcp.CallToolResult) string {
	if res == nil {
		return "<nil>"
	}
	return errResultBody(res)
}

func assertErrorContains(t *testing.T, res *mcp.CallToolResult, substr string) {
	t.Helper()
	body := errResultBody(res)
	if !strings.Contains(body, substr) {
		t.Fatalf("error body %q does not contain %q", body, substr)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
