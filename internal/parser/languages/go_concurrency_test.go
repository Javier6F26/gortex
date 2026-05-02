package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestGoConcurrency_BareGoroutineSpawn(t *testing.T) {
	src := `package foo

func worker() {}

func Run() {
	go worker()
}
`
	fix := runGoExtract(t, src)

	spawns := fix.edgesByKind[graph.EdgeSpawns]
	if len(spawns) != 1 {
		t.Fatalf("expected 1 EdgeSpawns, got %d: %+v", len(spawns), spawns)
	}
	e := spawns[0]
	if e.From != "pkg/foo.go::Run" {
		t.Errorf("from = %q", e.From)
	}
	if e.To != "unresolved::worker" {
		t.Errorf("to = %q", e.To)
	}
	if mode, _ := e.Meta["mode"].(string); mode != "goroutine" {
		t.Errorf("mode = %q, want goroutine", mode)
	}

	// EdgeCalls must still be present alongside the spawn.
	hasCall := false
	for _, ce := range fix.edgesByKind[graph.EdgeCalls] {
		if ce.From == e.From && ce.To == e.To {
			hasCall = true
		}
	}
	if !hasCall {
		t.Errorf("EdgeCalls should be emitted alongside EdgeSpawns")
	}
}

func TestGoConcurrency_MethodReceiverGoroutineSpawn(t *testing.T) {
	src := `package foo

type Server struct{}

func (s *Server) Tick() {}

func Start(s *Server) {
	go s.Tick()
}
`
	fix := runGoExtract(t, src)

	spawns := fix.edgesByKind[graph.EdgeSpawns]
	if len(spawns) != 1 {
		t.Fatalf("expected 1 EdgeSpawns, got %d: %+v", len(spawns), spawns)
	}
	if spawns[0].From != "pkg/foo.go::Start" {
		t.Errorf("from = %q", spawns[0].From)
	}
}

func TestGoConcurrency_RegularCallNotSpawn(t *testing.T) {
	src := `package foo

func worker() {}

func Run() {
	worker()
}
`
	fix := runGoExtract(t, src)

	if len(fix.edgesByKind[graph.EdgeSpawns]) != 0 {
		t.Errorf("regular call must not emit EdgeSpawns: %+v",
			fix.edgesByKind[graph.EdgeSpawns])
	}
}

func TestGoConcurrency_ClosureSpawn(t *testing.T) {
	// `go func() {...}()` — the closure itself is the spawn target.
	// The closure walker emits a KindClosure node for the
	// func_literal and an EdgeSpawns from the enclosing function to
	// that closure node. Calls inside the closure body (`println`)
	// run synchronously inside the goroutine and are NOT spawn sites.
	src := `package foo

func Run() {
	go func() {
		println("hi")
	}()
}
`
	fix := runGoExtract(t, src)

	closures := fix.nodesByKind[graph.KindClosure]
	if len(closures) != 1 {
		t.Fatalf("expected 1 closure, got %d", len(closures))
	}
	closureID := closures[0].ID

	spawns := fix.edgesByKind[graph.EdgeSpawns]
	if len(spawns) != 1 {
		t.Fatalf("expected 1 EdgeSpawns to the closure, got %d: %+v", len(spawns), spawns)
	}
	if spawns[0].From != "pkg/foo.go::Run" {
		t.Errorf("spawn from = %q", spawns[0].From)
	}
	if spawns[0].To != closureID {
		t.Errorf("spawn to = %q, want closure %q", spawns[0].To, closureID)
	}
}

func TestGoConcurrency_NonSpawnedClosureGetsNoSpawnEdge(t *testing.T) {
	// Closure assigned to a variable then invoked synchronously —
	// not a goroutine spawn, must not produce EdgeSpawns.
	src := `package foo

func Run() {
	fn := func() {
		println("hi")
	}
	fn()
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.edgesByKind[graph.EdgeSpawns]); got != 0 {
		t.Errorf("synchronous closure invocation must not emit EdgeSpawns, got %d", got)
	}
}
