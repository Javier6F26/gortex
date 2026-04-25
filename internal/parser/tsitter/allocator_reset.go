// Hot-path workaround: the upstream tree-sitter/go-tree-sitter
// (v0.25.0) registers a Go-routed allocator at package init —
// `ts_set_allocator(c_malloc_fn, …)` — and `c_malloc_fn` calls back
// into Go (`go_malloc`), which then calls `C.malloc`. Every internal
// allocation inside the parser therefore round-trips C → Go → C
// instead of going straight to libc. On large repos that round-trip
// dominated the cgo cost: `_cgoexp_…go_malloc` showed up at ~35% of
// total CPU in our indexer profile, and indexing the gortex repo
// itself ran 3× slower than the smacker baseline.
//
// We undo that by re-calling `ts_set_allocator(NULL, …, NULL)` from
// our own init, which the C library treats as "use libc directly."
// The symbol `ts_set_allocator` is exported from the upstream
// package's CGO archive, so we declare it `extern` and link against
// the same archive.
//
// Init ordering: Go runs init() functions in import-graph order, and
// this file is in the `tsitter` package which depends on the upstream
// `tree_sitter` package — so the upstream init (which registers the
// indirection) runs first, ours runs after, and our reset wins.
//
// Tree-sitter's docs warn that switching allocators after objects
// have been created is unsafe unless those objects are freed first
// or the new allocator shares state with the old. Both go-routed and
// libc allocators ultimately call `C.malloc`/`C.free`, so they share
// state — the round-trip layer drops away cleanly.

package tsitter

/*
extern void ts_set_allocator(
	void *(*new_malloc)(unsigned long),
	void *(*new_calloc)(unsigned long, unsigned long),
	void *(*new_realloc)(void *, unsigned long),
	void (*new_free)(void *)
);

static void gortex_use_libc_allocator(void) {
	ts_set_allocator(0, 0, 0, 0);
}
*/
import "C"

func init() {
	C.gortex_use_libc_allocator()
}
