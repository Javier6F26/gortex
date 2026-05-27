//go:build darwin

// Package store_ladybug exposes mallocTrim as a thin cgo shim over
// the platform's "return retained pages to the OS" entry point.
// Ladybug's native allocator keeps freed pages for fast reuse; on
// long-lived daemons the retained set grows monotonically and shows
// up as climbing physical_footprint even while RSS stays low. The
// shim is called from the high-volume query and drain paths after a
// large operation completes so the allocator's high-water mark
// settles back down.
package store_ladybug

// #include <malloc/malloc.h>
import "C"

// mallocTrim asks the system allocator to return retained pages to
// the OS. On Darwin the call routes to malloc_zone_pressure_relief
// on the default malloc zone. The "goal" argument of 0 means "free
// as much as you can"; the return value (bytes released) is ignored
// because the caller has nothing useful to do with it.
func mallocTrim() {
	C.malloc_zone_pressure_relief(C.malloc_default_zone(), 0)
}
