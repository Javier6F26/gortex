// Package persistence provides snapshot-based graph persistence with pluggable backends.
package persistence

import (
	"errors"
	"time"

	"github.com/zzet/gortex/internal/graph"
)

// ErrNotFound is returned when no snapshot exists for the given key.
var ErrNotFound = errors.New("persistence: snapshot not found")

// Snapshot is the unit of persistence — a complete graph state at a point in time.
// CurrentExtractionVersion is the version of the extraction OUTPUT schema. It
// is bumped ONLY when a binary actually changes what extraction produces (a new
// node/edge shape, a corrected graph) — never on a routine version bump. A warm
// snapshot stays reusable across binary releases that share this version, so a
// no-op release does not force the full cold rebuild a binary-version-string
// gate would. Raise it in lockstep with any change that alters indexed output.
const CurrentExtractionVersion = 1

type Snapshot struct {
	Version string `json:"version"`
	// ExtractionVersion is the CurrentExtractionVersion the snapshot was
	// produced with; warm reuse is gated on it, not the binary version string.
	ExtractionVersion int    `json:"extraction_version,omitempty"`
	RepoPath          string `json:"repo_path"`
	CommitHash        string `json:"commit_hash"`
	// Branch is the git branch the snapshot was taken on. Snapshots
	// are keyed by (repo, branch) so a slot survives commits on the
	// branch; empty for a detached HEAD (keyed by commit instead).
	Branch     string           `json:"branch,omitempty"`
	IndexedAt  time.Time        `json:"indexed_at"`
	Nodes      []*graph.Node    `json:"nodes"`
	Edges      []*graph.Edge    `json:"edges"`
	FileMtimes map[string]int64 `json:"file_mtimes"`

	// VectorIndex is the serialized HNSW vector index (nil when embeddings are disabled).
	VectorIndex []byte `json:"vector_index,omitempty"`
	// VectorDims is the embedding dimensionality (0 when embeddings are disabled).
	VectorDims int `json:"vector_dims,omitempty"`
	// VectorCount is the number of vectors in the index.
	VectorCount int `json:"vector_count,omitempty"`
}

// Store is the pluggable persistence backend interface.
// Implementations must be safe for sequential use (not required to be concurrent).
type Store interface {
	// Check returns true if a snapshot exists for the (repo, branch)
	// slot. commitHash is used only as the slot key for a detached
	// HEAD (empty branch).
	Check(repoPath, branch, commitHash string) bool

	// Load deserializes and returns the snapshot for the (repo,
	// branch) slot. Returns ErrNotFound if no snapshot exists.
	Load(repoPath, branch, commitHash string) (*Snapshot, error)

	// Save serializes the snapshot to persistent storage.
	Save(snap *Snapshot) error

	// Validate checks that an existing snapshot is compatible with
	// the current gortex version. Returns false on version mismatch.
	Validate(repoPath, branch, commitHash string) bool

	// Evict removes the snapshot for the (repo, branch) slot.
	Evict(repoPath, branch, commitHash string) error

	// Close releases any resources held by the backend.
	Close() error
}
