//go:build !darwin && !linux

// Package store_ladybug exposes mallocTrim as a thin cgo shim over
// the platform's "return retained pages to the OS" entry point.
// Ladybug's native allocator keeps freed pages for fast reuse; on
// long-lived daemons the retained set grows monotonically and shows
// up as climbing physical_footprint even while RSS stays low. The
// shim is called from the high-volume query and drain paths after a
// large operation completes so the allocator's high-water mark
// settles back down.
package store_ladybug

// mallocTrim is a no-op on platforms without a documented "return
// retained pages" entry point. Windows reclaims via the heap
// manager's own background trimming and *BSDs use jemalloc tweakable
// through MALLOC_OPTIONS rather than a C entry point — both leave
// the caller no actionable hook.
func mallocTrim() {}
