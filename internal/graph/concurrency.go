package graph

import "strings"

// ConcurrencyAnnotation carries two cheap, high-signal concurrency
// facts about a symbol — typically a caller surfaced by a navigation
// query. Both flags default to false; the *Why fields are populated
// only when the matching flag is true, so a zero-value annotation is
// safe to attach unconditionally and to omit from a response.
type ConcurrencyAnnotation struct {
	// SyncGuarded is true when the symbol is a method (or a closure
	// defined on a method-bearing type) whose receiver / parent type
	// holds a lock — a sync.Mutex / sync.RWMutex field in Go, a Mutex
	// / RwLock field in Rust, a ReentrantLock field in Java, a
	// SemaphoreSlim-typed field in C#. Calls made from inside such a
	// type are presumptively lock-protected, which matters when
	// reasoning about whether a change is concurrency-safe.
	SyncGuarded    bool   `json:"sync_guarded,omitempty"`
	SyncGuardedWhy string `json:"sync_guarded_why,omitempty"`
	// CrossConcurrent is true when the symbol is launched across a
	// concurrency boundary — it is the target of an EdgeSpawns edge,
	// i.e. a `go` statement / goroutine closure in Go, an async / Promise
	// / worker entry in JS-TS, a threading.Thread / async def in
	// Python, a spawned thread in Rust. Any call the symbol makes runs
	// on a different goroutine / thread than its lexical parent.
	CrossConcurrent    bool   `json:"cross_concurrent,omitempty"`
	CrossConcurrentWhy string `json:"cross_concurrent_why,omitempty"`
}

// Any reports whether either concurrency flag is set. Callers use it
// to decide whether an annotation is worth attaching / serialising.
func (c ConcurrencyAnnotation) Any() bool {
	return c.SyncGuarded || c.CrossConcurrent
}

// ClassifyConcurrency derives the concurrency-safety annotation for a
// node. It reads through the Reader contract — so it works unchanged
// against a base graph or a per-session overlay view — and does no
// parser work: it reuses substrate that existing extractors already
// emit:
//
//   - EdgeMemberOf links a method / field / closure to its parent
//     type or enclosing function.
//   - EdgeSpawns links a caller to a function / closure it launches
//     asynchronously (goroutine, async, promise, worker pool).
//   - A field node carries Meta["field_type"] with the verbatim
//     declared type text.
//
// Language coverage:
//
//   - sync_guarded relies on typed field nodes. Go, Rust, Java, and C#
//     emit KindField nodes with Meta["field_type"], so a lock-holding
//     receiver type is detected for those languages. TypeScript and
//     PHP model class properties as KindVariable without a typed
//     field-type, and Python does not materialise instance attributes
//     as nodes at all — sync_guarded is therefore not reported for
//     those languages (it stays false rather than guessing).
//   - cross_concurrent relies only on EdgeSpawns and so covers every
//     language whose extractor emits spawn edges (Go full; TS / Python
//     / Rust / Kotlin / C# for the spawn patterns they detect).
//
// An unknown / missing node yields a zero-value annotation.
func ClassifyConcurrency(r Reader, nodeID string) ConcurrencyAnnotation {
	var ann ConcurrencyAnnotation
	if r == nil || r.GetNode(nodeID) == nil {
		return ann
	}
	if why := spawnedAsConcurrent(r, nodeID); why != "" {
		ann.CrossConcurrent = true
		ann.CrossConcurrentWhy = why
	}
	if field, typeName := receiverLockField(r, r.GetNode(nodeID)); field != "" {
		ann.SyncGuarded = true
		ann.SyncGuardedWhy = "receiver type " + typeName +
			" holds a lock (" + field + "); calls from here are presumptively lock-protected"
	}
	return ann
}

// spawnedAsConcurrent returns a human-readable explanation when the
// node is the target of at least one EdgeSpawns edge, and "" otherwise.
// The explanation names the spawn mode (goroutine / async / promise /
// worker_pool) recorded on the edge's Meta when available.
func spawnedAsConcurrent(r Reader, nodeID string) string {
	for _, e := range r.GetInEdges(nodeID) {
		if e.Kind != EdgeSpawns {
			continue
		}
		mode, _ := e.Meta["mode"].(string)
		switch mode {
		case "goroutine":
			return "launched as a goroutine — runs on a different goroutine than its caller"
		case "async":
			return "launched as an async task — runs off the caller's synchronous path"
		case "promise":
			return "launched inside a promise — runs off the caller's synchronous path"
		case "worker_pool":
			return "dispatched to a worker pool — runs on a pool thread, not the caller's"
		default:
			return "launched across a concurrency boundary (spawned), not called synchronously"
		}
	}
	return ""
}

// receiverLockField finds the parent / receiver type of a method (or a
// closure whose enclosing scope is a method-bearing type) and reports
// the first lock-typed field declared on that type. Returns the field
// name and the type name, or ("", "") when the node is not a method,
// has no resolvable receiver type, or the type holds no lock.
func receiverLockField(r Reader, n *Node) (field, typeName string) {
	if n == nil || (n.Kind != KindMethod && n.Kind != KindClosure) {
		return "", ""
	}
	// A method (or closure) reaches its owner through EdgeMemberOf.
	// For a closure the owner is usually the enclosing function, not a
	// type — receiverTypeOf walks one extra hop in that case so a
	// closure defined inside a method still resolves to the method's
	// receiver type.
	typeNode := receiverTypeOf(r, n)
	if typeNode == nil {
		return "", ""
	}
	// A type's fields point at it via EdgeMemberOf; walk the inbound
	// member_of edges and inspect each field's declared type.
	for _, e := range r.GetInEdges(typeNode.ID) {
		if e.Kind != EdgeMemberOf {
			continue
		}
		fn := r.GetNode(e.From)
		if fn == nil || fn.Kind != KindField {
			continue
		}
		ft, _ := fn.Meta["field_type"].(string)
		if isLockTypeName(ft) {
			return fn.Name, typeNode.Name
		}
	}
	return "", ""
}

// receiverTypeOf resolves the type a method / closure belongs to. A
// method points straight at its receiver type via EdgeMemberOf. A
// closure points at its enclosing function / method; when that owner
// is itself a method the walk takes one more EdgeMemberOf hop so a
// closure spawned inside a method is still attributed to the method's
// receiver type. Returns nil when no KindType / KindInterface owner is
// reachable within those two hops.
func receiverTypeOf(r Reader, n *Node) *Node {
	for _, e := range r.GetOutEdges(n.ID) {
		if e.Kind != EdgeMemberOf {
			continue
		}
		owner := r.GetNode(e.To)
		if owner == nil {
			continue
		}
		if owner.Kind == KindType || owner.Kind == KindInterface {
			return owner
		}
		// Closure → enclosing method → receiver type (second hop).
		if n.Kind == KindClosure && owner.Kind == KindMethod {
			for _, e2 := range r.GetOutEdges(owner.ID) {
				if e2.Kind != EdgeMemberOf {
					continue
				}
				t := r.GetNode(e2.To)
				if t != nil && (t.Kind == KindType || t.Kind == KindInterface) {
					return t
				}
			}
		}
	}
	return nil
}

// isLockTypeName reports whether a declared field-type string names a
// mutual-exclusion primitive. Matching is on the trailing type name so
// package / module qualifiers (`sync.Mutex`, `tokio::sync::Mutex`,
// `std::sync::RwLock`, `java.util.concurrent.locks.ReentrantLock`) and
// a single leading pointer / reference marker do not defeat it.
// Recognised across Go, Rust, Java, and C# — the languages whose
// extractors emit typed KindField nodes.
func isLockTypeName(fieldType string) bool {
	t := strings.TrimSpace(fieldType)
	if t == "" {
		return false
	}
	t = strings.TrimPrefix(t, "*") // Go pointer-to-mutex
	t = strings.TrimPrefix(t, "&") // Rust reference
	// Drop a generic parameter list — Rust's Mutex<T> / RwLock<T>,
	// C#'s lock wrappers — so the bare type name is what we test.
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	// Reduce a qualified path to its trailing segment.
	for _, sep := range []string{"::", "."} {
		if i := strings.LastIndex(t, sep); i >= 0 {
			t = t[i+len(sep):]
		}
	}
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "mutex", "rwmutex", // Go sync.Mutex / sync.RWMutex
		"rwlock",                 // Rust std::sync::RwLock
		"reentrantlock",          // Java ReentrantLock
		"reentrantreadwritelock", // Java ReentrantReadWriteLock
		"readwritelock", "lock",  // Java Lock / ReadWriteLock interfaces
		"semaphore",            // Java / C# Semaphore
		"semaphoreslim",        // C# SemaphoreSlim
		"readerwriterlockslim", // C# ReaderWriterLockSlim
		"spinlock":             // Rust spin::Mutex-style / C# SpinLock
		return true
	}
	return false
}
