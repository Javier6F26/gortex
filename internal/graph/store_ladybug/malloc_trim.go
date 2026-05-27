package store_ladybug

// mallocTrimRowThreshold guards every mallocTrim caller — the trim
// itself takes a low-millisecond hop into C and a kernel
// madvise(MADV_FREE) per zone, so per-call overhead matters. The
// threshold should fire on the drains / queries that actually move
// the allocator's high-water mark, not on the rapid-fire low-row
// queries the daemon's steady state runs. Picked from observation:
// at 50k rows a single capability call materialises hundreds of
// kilobytes of C strings worth releasing; below that the released
// pages aren't a measurable share of physical_footprint.
const mallocTrimRowThreshold = 50000
