package mcp

import (
	"fmt"
	"sort"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
)

// ToolScope is the per-tool scope kind. Wire-format encodes this on
// every tool definition (`scope` field on the GCX1 schema). The
// in-memory registry below is the server-side equivalent: it lets the
// dispatcher validate `repo` per call without re-deriving the scope
// from the tool's name on every request.
//
// Three kinds, exhaustive:
//
//   - ScopeRepo — operates against one project's index. Requires
//     `repo: <string>`. Missing or list = protocol error.
//   - ScopeWorkspace — operates at the workspace level. `repo` MUST
//     NOT appear. Present = protocol error.
//   - ScopeFanOut — spans several indexes. Requires
//     `repo: <list>` (non-empty, including the `["*"]` sentinel).
//     Missing, empty, or non-list = protocol error.
//
// The active workspace boundary is enforced at the graph level by the
// daemon's per-session scope (sessionScope / nodeInSessionScope); this
// layer validates the wire shape of `repo` per the tool's declared scope.
type ToolScope int

const (
	// ScopeRepo — `repo: <string>` required.
	ScopeRepo ToolScope = iota + 1
	// ScopeWorkspace — no `repo` parameter. Operates against the
	// active workspace as a unit.
	ScopeWorkspace
	// ScopeFanOut — `repo: <list>` required.
	ScopeFanOut
)

// String returns the wire-form scope name (matches gcx-go encoding).
func (s ToolScope) String() string {
	switch s {
	case ScopeRepo:
		return "repo"
	case ScopeWorkspace:
		return "workspace"
	case ScopeFanOut:
		return "fan-out"
	default:
		return "unknown"
	}
}

// FanOutAllSentinel is the wire-format `["*"]` sentinel.
const FanOutAllSentinel = "*"

// scopeRegistry is the per-Server tool-name → scope map. Populated as
// tools are registered via RegisterToolScope; consulted by
// ResolveScopedRepos before any handler runs. Concurrent-safe so the
// daemon path can register tools after construction without surprises.
type scopeRegistry struct {
	mu     sync.RWMutex
	scopes map[string]ToolScope
}

func newScopeRegistry() *scopeRegistry {
	return &scopeRegistry{scopes: make(map[string]ToolScope)}
}

// set assigns scope to toolName. Last write wins — re-registering a
// tool with a different scope is allowed (used by tests) but logged at
// the call site.
func (r *scopeRegistry) set(toolName string, scope ToolScope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes[toolName] = scope
}

// get returns the scope for toolName and whether it was registered.
func (r *scopeRegistry) get(toolName string) (ToolScope, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.scopes[toolName]
	return s, ok
}

// snapshot returns a copy of the registry as a tool-name → scope-name
// map, sorted by name. Used by `list_repos`-shaped diagnostics and by
// tests that want to assert no tool has been left unclassified.
func (r *scopeRegistry) snapshot() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.scopes))
	for name, scope := range r.scopes {
		out[name] = scope.String()
	}
	return out
}

// allTools returns the registered tool names sorted lexically.
func (r *scopeRegistry) allTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.scopes))
	for name := range r.scopes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ScopedRepos is the resolved repo set for a single MCP tool call. The
// kind matches the tool's declared scope; Repos lists the concrete
// member names the handler should query.
type ScopedRepos struct {
	Kind ToolScope
	// Repos is the resolved set the handler should target. For
	// ScopeRepo it contains exactly one name. For ScopeFanOut it
	// contains the explicit subset. ScopeWorkspace carries none (the
	// handler reports the session's resolved workspace itself).
	Repos []string
}

// ResolveScopedRepos validates a request against the tool's declared
// scope and returns the concrete repo list to query, or a structured
// protocol error suitable for the caller to surface verbatim.
//
// The repo argument is the union value the wire-format slice decoded
// from the request: nil means "absent", a string means "single-repo
// shape", a slice means "fan-out shape". Callers wrap their
// `req.GetArguments()["repo"]` once and forward to us.
func ResolveScopedRepos(scope ToolScope, repo any) (*ScopedRepos, *mcp.CallToolResult) {
	switch scope {
	case ScopeRepo:
		return resolveRepoScope(repo)
	case ScopeWorkspace:
		return resolveWorkspaceScope(repo)
	case ScopeFanOut:
		return resolveFanOutScope(repo)
	default:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   fmt.Sprintf("unknown tool scope %v", scope),
		})
	}
}

// resolveRepoScope: scope=repo. Wire-format shape: `repo: <string>`
// REQUIRED. Missing or list = protocol error.
func resolveRepoScope(repo any) (*ScopedRepos, *mcp.CallToolResult) {
	switch v := repo.(type) {
	case nil:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `scope "repo" requires a string "repo" parameter; name the project explicitly`,
			Data:      map[string]any{"scope": "repo", "expected": "string"},
		})
	case string:
		if v == "" {
			return nil, NewStructuredErrorResult(StructuredError{
				ErrorCode: ErrCodeInvalidArgument,
				Message:   `"repo" must not be the empty string`,
				Data:      map[string]any{"scope": "repo"},
			})
		}
		return &ScopedRepos{Kind: ScopeRepo, Repos: []string{v}}, nil
	case []any, []string:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `scope "repo" requires a single string for "repo"; got a list — use a fan-out tool if you mean to query multiple projects`,
			Data:      map[string]any{"scope": "repo", "got": "list"},
		})
	default:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   fmt.Sprintf(`scope "repo" requires a string "repo"; got %T`, repo),
			Data:      map[string]any{"scope": "repo"},
		})
	}
}

// resolveWorkspaceScope: scope=workspace. Wire-format shape: `repo` MUST
// NOT appear. The concrete member set is reported by the handler from
// the session's resolved workspace, not derived here.
func resolveWorkspaceScope(repo any) (*ScopedRepos, *mcp.CallToolResult) {
	if repo != nil {
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `scope "workspace" tools must not pass "repo"; the tool operates on the active workspace as a unit`,
			Data:      map[string]any{"scope": "workspace"},
		})
	}
	return &ScopedRepos{Kind: ScopeWorkspace}, nil
}

// resolveFanOutScope: scope=fan-out. Wire-format shape:
// `repo: <non-empty list>`.
func resolveFanOutScope(repo any) (*ScopedRepos, *mcp.CallToolResult) {
	switch v := repo.(type) {
	case nil:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `scope "fan-out" requires a non-empty list "repo"; for breadth use ["*"], for a subset name them explicitly`,
			Data:      map[string]any{"scope": "fan-out", "expected": "list"},
		})
	case string:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `scope "fan-out" requires a list "repo"; got a single string`,
			Data:      map[string]any{"scope": "fan-out", "got": "string"},
		})
	case []any:
		names := make([]string, 0, len(v))
		for i, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, NewStructuredErrorResult(StructuredError{
					ErrorCode: ErrCodeInvalidArgument,
					Message:   fmt.Sprintf(`"repo"[%d] must be a string, got %T`, i, e),
				})
			}
			names = append(names, s)
		}
		return resolveFanOutNames(names)
	case []string:
		return resolveFanOutNames(v)
	default:
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   fmt.Sprintf(`scope "fan-out" requires a list "repo"; got %T`, repo),
		})
	}
}

// resolveFanOutNames is the inner stage of fan-out resolution. Rejects
// empty lists and a mixed `["*", "x"]` shape; the `["*"]` sentinel is
// expanded by the handler against the session's resolved workspace.
func resolveFanOutNames(names []string) (*ScopedRepos, *mcp.CallToolResult) {
	if len(names) == 0 {
		return nil, NewStructuredErrorResult(StructuredError{
			ErrorCode: ErrCodeInvalidArgument,
			Message:   `"repo" must be a non-empty list; pass ["*"] for the full workspace`,
		})
	}

	// `["*"]` sentinel: must be the only element; mixed `["*", "x"]`
	// is rejected to keep the wire shape unambiguous.
	for _, n := range names {
		if n == FanOutAllSentinel && len(names) != 1 {
			return nil, NewStructuredErrorResult(StructuredError{
				ErrorCode: ErrCodeInvalidArgument,
				Message:   `"repo" cannot mix the ["*"] sentinel with named entries`,
			})
		}
	}

	// Deduplicate while preserving first-seen order so the handler's
	// fan-out is stable for caching / logging.
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return &ScopedRepos{Kind: ScopeFanOut, Repos: out}, nil
}
