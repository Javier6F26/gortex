// Package pointer is a drop-in, sharded replacement for
// github.com/mattn/go-pointer. The upstream implementation guards a
// single map[unsafe.Pointer]any with one global RWMutex; every CGo
// payload-passing call site (notably tree-sitter's
// Parser.ParseWithOptions) takes that lock. Under parallel parsing
// load that single mutex becomes the throughput ceiling — the
// daemon's cold-start profile showed 71 of ~100 goroutines blocked
// on it at any moment.
//
// This implementation keeps the upstream API verbatim (Save / Restore
// / Unref with the same signatures) so we can swap it in via a
// go.mod `replace` directive without touching go-tree-sitter or any
// other dependency. The store is split into numShards independent
// (mutex, map) pairs; the shard for a given pointer is chosen from
// the pointer's address bits, so concurrent Save calls naturally
// distribute across shards because C.malloc returns distinct
// addresses for distinct allocations.
//
// numShards is intentionally over-provisioned (64) — the cost per
// shard is the unused empty map header plus a zero-value Mutex, and
// the parser worker pool is sized to NumCPU which on modern boxes
// can sit at 12–16; having room beyond that means even a future
// expansion of the worker pool finds free shards.
package pointer

// #include <stdlib.h>
import "C"

import (
	"sync"
	"unsafe"
)

const numShards = 64

type shard struct {
	mu    sync.Mutex
	store map[unsafe.Pointer]any
}

var shards [numShards]shard

func init() {
	for i := range shards {
		shards[i].store = make(map[unsafe.Pointer]any)
	}
}

// shardFor picks a shard from the pointer's address. C.malloc results
// are at least 8-byte aligned on every platform Go supports, so
// shifting off the low 3 bits removes the alignment artefact and the
// modulus then spreads addresses across all 64 shards.
func shardFor(ptr unsafe.Pointer) *shard {
	return &shards[(uintptr(ptr)>>3)%numShards]
}

// Save stores v and returns a unique C pointer that can be passed
// across CGo and later resolved back to v via Restore. Returns nil
// when v is nil to match the upstream contract used by callers that
// pass optional payloads through.
func Save(v any) unsafe.Pointer {
	if v == nil {
		return nil
	}

	// One-byte malloc gives us a unique, valid C pointer to use as the
	// store key. The C side never dereferences the byte — it only
	// passes the pointer back to our Restore callback. The body of
	// the value v lives in Go memory; this pointer is just the index.
	ptr := unsafe.Pointer(C.malloc(C.size_t(1)))
	if ptr == nil {
		panic("go-pointer: malloc returned nil")
	}

	s := shardFor(ptr)
	s.mu.Lock()
	s.store[ptr] = v
	s.mu.Unlock()
	return ptr
}

// Restore returns the value previously stored under ptr by Save, or
// nil when ptr is nil or has been Unref'd.
func Restore(ptr unsafe.Pointer) any {
	if ptr == nil {
		return nil
	}
	s := shardFor(ptr)
	s.mu.Lock()
	v := s.store[ptr]
	s.mu.Unlock()
	return v
}

// Unref drops the value stored under ptr and frees the underlying C
// allocation. Safe to call with nil. Pairs 1:1 with Save — callers
// typically defer Unref(ptr) right after Save.
func Unref(ptr unsafe.Pointer) {
	if ptr == nil {
		return
	}
	s := shardFor(ptr)
	s.mu.Lock()
	delete(s.store, ptr)
	s.mu.Unlock()
	C.free(ptr)
}
