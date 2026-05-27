//go:build linux

// Package store_ladybug exposes mallocTrim as a thin cgo shim over
// the platform's "return retained pages to the OS" entry point.
// Ladybug's native allocator keeps freed pages for fast reuse; on
// long-lived daemons the retained set grows monotonically and shows
// up as climbing physical_footprint even while RSS stays low. The
// shim is called from the high-volume query and drain paths after a
// large operation completes so the allocator's high-water mark
// settles back down.
package store_ladybug

// #include <malloc.h>
import "C"

// mallocTrim asks glibc to release free heap pages back to the OS.
// pad of 0 means "no top padding"; the return value is whether any
// memory was actually released and is ignored.
func mallocTrim() {
	C.malloc_trim(0)
}
