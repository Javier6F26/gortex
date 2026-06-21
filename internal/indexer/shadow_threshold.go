package indexer

import (
	"os"
	"strconv"
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// defaultShadowMaxFileCount caps the file count above which IndexCtx
// refuses to swap idx.graph for an in-memory shadow during cold start.
// Picked empirically from the in-memory store's prior profiling: at
// ~35k C files (drivers/) the in-memory store peaked at 8.6GB RSS; at
// 60k+ the peak is well past 16GB. The shadow path doubles that
// footprint (in-memory + persisted disk copy at the FlushBulk step),
// so the safe ceiling for a 32GB dev machine sits around 50k source
// files. Above that we fall through to the per-call disk path —
// slower per IndexCtx but bounded RAM.
const defaultShadowMaxFileCount = 50000

// defaultStreamingChunkSize is the per-chunk file count used by the
// streaming-flush path. At ~30 nodes / ~100 edges per file, 5000
// files per chunk yields a ~600MB shadow that fits comfortably in
// RAM even on 8GB build agents.
const defaultStreamingChunkSize = 5000

// shadowMaxFileCount returns the active file-count ceiling for the
// IndexCtx in-memory shadow swap. GORTEX_SHADOW_MAX_FILES overrides
// the default; setting it to 0 disables the shadow entirely (always
// run against the disk store directly), setting it to a high value
// (e.g. 10_000_000) effectively disables the guard. Non-numeric or
// negative values fall back to the default.
func shadowMaxFileCount() int {
	if v := os.Getenv("GORTEX_SHADOW_MAX_FILES"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n >= 0 {
			return n
		}
	}
	return defaultShadowMaxFileCount
}

// defaultShadowMaxBytes caps the total raw input bytes above which the
// in-memory shadow swap is refused even when the file count is under
// shadowMaxFileCount. The file-count ceiling alone is blind to the
// few-huge-files shape (a content repo of a few hundred PDFs / text
// dumps / spreadsheets is well under 50k files but can hold multiple
// GB of bytes that explode into hundreds of thousands of section
// nodes). The all-in-memory shadow holds the whole post-parse graph —
// nodes, edges, retained section text, the in-process search index —
// for the entire pipeline, so a multi-GB input pins multiples of that
// in RAM and OOMs on a content-heavy repo (see #120). 1 GiB of input
// keeps a normal source tree on the fast shadow path while routing
// content-heavy / data-dump repos to the bounded per-call disk path.
const defaultShadowMaxBytes int64 = 1 << 30 // 1 GiB

// shadowMaxBytes returns the active total-input-byte ceiling for the
// in-memory shadow swap. GORTEX_SHADOW_MAX_BYTES overrides the
// default; a high value (e.g. 1<<60) effectively disables the byte
// guard, leaving only the file-count ceiling. Non-numeric, zero, or
// negative values fall back to the default.
func shadowMaxBytes() int64 {
	if v := os.Getenv("GORTEX_SHADOW_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err == nil && n > 0 {
			return n
		}
	}
	return defaultShadowMaxBytes
}

// streamingFlushActive reports whether the streaming-flush parse path
// should engage for this IndexCtx. Requirements:
//
//   - the backing store implements graph.BulkLoader (the on-disk backend does)
//   - the file count is above the shadow-max threshold (small repos
//     stay on the all-in-memory shadow path)
//   - GORTEX_STREAMING_FLUSH is enabled (off by default — the
//     streaming path leaves resolve to the disk-only per-call path,
//     so it's only useful when shadow swap can't fit in RAM)
func streamingFlushActive(store graph.Store, fileCount int) bool {
	if _, ok := store.(graph.BulkLoader); !ok {
		return false
	}
	if fileCount <= shadowMaxFileCount() {
		return false
	}
	v := os.Getenv("GORTEX_STREAMING_FLUSH")
	return v == "1" || strings.EqualFold(v, "true")
}

// streamingChunkSize returns the per-chunk file count for the
// streaming-flush path. Override via GORTEX_STREAMING_CHUNK_SIZE.
func streamingChunkSize() int {
	if v := os.Getenv("GORTEX_STREAMING_CHUNK_SIZE"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			return n
		}
	}
	return defaultStreamingChunkSize
}
