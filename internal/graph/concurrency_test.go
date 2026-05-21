package graph

import "testing"

// addMethodNode / addTypeNode / addFieldNode / addEdge are tiny
// builders so each test case can pin exactly which nodes and edges
// exist. The classifier is graph-only, so a hand-built graph is the
// right fixture — no parser in the loop.
func addNode(g *Graph, id string, kind NodeKind, meta map[string]any) {
	g.AddNode(&Node{ID: id, Kind: kind, Name: shortName(id), FilePath: "x.go", Meta: meta})
}

func shortName(id string) string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '.' || id[i] == ':' {
			return id[i+1:]
		}
	}
	return id
}

func addCEdge(g *Graph, from, to string, kind EdgeKind, meta map[string]any) {
	g.AddEdge(&Edge{From: from, To: to, Kind: kind, FilePath: "x.go", Origin: OriginASTResolved, Meta: meta})
}

func TestIsLockTypeName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"go mutex", "sync.Mutex", true},
		{"go rwmutex", "sync.RWMutex", true},
		{"go pointer mutex", "*sync.Mutex", true},
		{"go embedded mutex", "Mutex", true},
		{"rust mutex generic", "Mutex<State>", true},
		{"rust qualified rwlock", "std::sync::RwLock<Data>", true},
		{"rust ref mutex", "&Mutex<u32>", true},
		{"java reentrant lock", "ReentrantLock", true},
		{"java qualified rwlock", "java.util.concurrent.locks.ReentrantReadWriteLock", true},
		{"java lock iface", "Lock", true},
		{"csharp semaphoreslim", "SemaphoreSlim", true},
		{"csharp rwlockslim", "ReaderWriterLockSlim", true},
		{"plain int not a lock", "int", false},
		{"string not a lock", "string", false},
		{"map not a lock", "map[string]int", false},
		{"empty", "", false},
		{"unrelated struct", "bytes.Buffer", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLockTypeName(tc.in); got != tc.want {
				t.Errorf("isLockTypeName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestClassifyConcurrency_SyncGuarded is the table-driven check that a
// method on a mutex-holding type is flagged sync_guarded and a method
// on a lock-free type is not.
func TestClassifyConcurrency_SyncGuarded(t *testing.T) {
	cases := []struct {
		name      string
		fieldType string // declared type of the receiver type's single field
		fieldKind NodeKind
		want      bool
	}{
		{"go mutex field", "sync.Mutex", KindField, true},
		{"go rwmutex field", "sync.RWMutex", KindField, true},
		{"go embedded mutex", "Mutex", KindField, true},
		{"rust mutex field", "Mutex<State>", KindField, true},
		{"java reentrant lock field", "ReentrantLock", KindField, true},
		{"lock-free int field", "int", KindField, false},
		{"lock-free string field", "string", KindField, false},
		// A class property modelled as KindVariable (TS / PHP) is not
		// a KindField, so it must not be picked up — sync_guarded is
		// honestly not reported for those languages.
		{"variable-modelled property ignored", "Mutex", KindVariable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			addNode(g, "p.go::Store", KindType, nil)
			addNode(g, "p.go::Store.Get", KindMethod, nil)
			addCEdge(g, "p.go::Store.Get", "p.go::Store", EdgeMemberOf, nil)
			addNode(g, "p.go::Store.f", tc.fieldKind, map[string]any{"field_type": tc.fieldType})
			addCEdge(g, "p.go::Store.f", "p.go::Store", EdgeMemberOf, nil)

			ann := ClassifyConcurrency(g, "p.go::Store.Get")
			if ann.SyncGuarded != tc.want {
				t.Errorf("SyncGuarded = %v, want %v", ann.SyncGuarded, tc.want)
			}
			if tc.want && ann.SyncGuardedWhy == "" {
				t.Error("SyncGuarded true but SyncGuardedWhy is empty")
			}
			if !tc.want && ann.SyncGuardedWhy != "" {
				t.Errorf("SyncGuarded false but SyncGuardedWhy = %q", ann.SyncGuardedWhy)
			}
		})
	}
}

// TestClassifyConcurrency_PlainFunctionNeverGuarded confirms a free
// function (no receiver type) is never flagged sync_guarded.
func TestClassifyConcurrency_PlainFunctionNeverGuarded(t *testing.T) {
	g := New()
	addNode(g, "p.go::doWork", KindFunction, nil)
	ann := ClassifyConcurrency(g, "p.go::doWork")
	if ann.SyncGuarded {
		t.Error("a plain function must not be sync_guarded")
	}
}

// TestClassifyConcurrency_CrossConcurrent checks that a symbol that is
// the target of an EdgeSpawns edge is flagged cross_concurrent and a
// plainly-called symbol is not.
func TestClassifyConcurrency_CrossConcurrent(t *testing.T) {
	cases := []struct {
		name      string
		spawnMode string // "" means no spawn edge — plain call
		spawned   bool
		want      bool
		wantWord  string // substring expected in the explanation
	}{
		{"goroutine spawn", "goroutine", true, true, "goroutine"},
		{"async spawn", "async", true, true, "async"},
		{"promise spawn", "promise", true, true, "promise"},
		{"worker pool spawn", "worker_pool", true, true, "worker pool"},
		{"spawn without mode meta", "", true, true, "spawned"},
		{"plain call only", "", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			addNode(g, "p.go::Caller", KindFunction, nil)
			addNode(g, "p.go::Target", KindFunction, nil)
			// A plain call edge is always present; it must never on
			// its own make the target cross_concurrent.
			addCEdge(g, "p.go::Caller", "p.go::Target", EdgeCalls, nil)
			if tc.spawned {
				var meta map[string]any
				if tc.spawnMode != "" {
					meta = map[string]any{"mode": tc.spawnMode}
				}
				addCEdge(g, "p.go::Caller", "p.go::Target", EdgeSpawns, meta)
			}
			ann := ClassifyConcurrency(g, "p.go::Target")
			if ann.CrossConcurrent != tc.want {
				t.Errorf("CrossConcurrent = %v, want %v", ann.CrossConcurrent, tc.want)
			}
			if tc.want {
				if ann.CrossConcurrentWhy == "" {
					t.Error("CrossConcurrent true but explanation empty")
				}
				if tc.wantWord != "" && !contains(ann.CrossConcurrentWhy, tc.wantWord) {
					t.Errorf("explanation %q missing %q", ann.CrossConcurrentWhy, tc.wantWord)
				}
			} else if ann.CrossConcurrentWhy != "" {
				t.Errorf("CrossConcurrent false but explanation = %q", ann.CrossConcurrentWhy)
			}
		})
	}
}

// TestClassifyConcurrency_ClosureInheritsReceiverType verifies that a
// closure spawned inside a method resolves to the method's receiver
// type, so a goroutine-launched closure on a mutex-holding type is
// flagged sync_guarded.
func TestClassifyConcurrency_ClosureInheritsReceiverType(t *testing.T) {
	g := New()
	addNode(g, "p.go::Store", KindType, nil)
	addNode(g, "p.go::Store.mu", KindField, map[string]any{"field_type": "sync.Mutex"})
	addCEdge(g, "p.go::Store.mu", "p.go::Store", EdgeMemberOf, nil)
	addNode(g, "p.go::Store.Run", KindMethod, nil)
	addCEdge(g, "p.go::Store.Run", "p.go::Store", EdgeMemberOf, nil)
	// Closure defined inside the method; member_of points at the method.
	addNode(g, "p.go::Store.Run#closure@10", KindClosure, nil)
	addCEdge(g, "p.go::Store.Run#closure@10", "p.go::Store.Run", EdgeMemberOf, nil)
	// The method launches the closure as a goroutine.
	addCEdge(g, "p.go::Store.Run", "p.go::Store.Run#closure@10", EdgeSpawns,
		map[string]any{"mode": "goroutine"})

	ann := ClassifyConcurrency(g, "p.go::Store.Run#closure@10")
	if !ann.SyncGuarded {
		t.Error("closure on a mutex-holding receiver type must be sync_guarded")
	}
	if !ann.CrossConcurrent {
		t.Error("goroutine-launched closure must be cross_concurrent")
	}
}

// TestClassifyConcurrency_BothFlags confirms the two flags are
// independent — a goroutine-launched method on a mutex type carries
// both.
func TestClassifyConcurrency_BothFlags(t *testing.T) {
	g := New()
	addNode(g, "p.go::Cache", KindType, nil)
	addNode(g, "p.go::Cache.lock", KindField, map[string]any{"field_type": "sync.RWMutex"})
	addCEdge(g, "p.go::Cache.lock", "p.go::Cache", EdgeMemberOf, nil)
	addNode(g, "p.go::Cache.refresh", KindMethod, nil)
	addCEdge(g, "p.go::Cache.refresh", "p.go::Cache", EdgeMemberOf, nil)
	addNode(g, "p.go::Manager.Start", KindMethod, nil)
	addCEdge(g, "p.go::Manager.Start", "p.go::Cache.refresh", EdgeSpawns,
		map[string]any{"mode": "goroutine"})

	ann := ClassifyConcurrency(g, "p.go::Cache.refresh")
	if !ann.SyncGuarded || !ann.CrossConcurrent {
		t.Errorf("expected both flags set, got sync_guarded=%v cross_concurrent=%v",
			ann.SyncGuarded, ann.CrossConcurrent)
	}
	if !ann.Any() {
		t.Error("Any() must be true when flags are set")
	}
}

// TestClassifyConcurrency_MissingNode returns a zero-value annotation
// for an unknown node, and a nil reader is also tolerated.
func TestClassifyConcurrency_MissingNode(t *testing.T) {
	g := New()
	if ann := ClassifyConcurrency(g, "p.go::nope"); ann.Any() {
		t.Error("unknown node must yield a zero-value annotation")
	}
	if ann := ClassifyConcurrency(nil, "p.go::nope"); ann.Any() {
		t.Error("nil reader must yield a zero-value annotation")
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
