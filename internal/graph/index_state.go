package graph

// RepoIndexState is the per-repo freshness provenance recorded at the
// end of a (re)index: the git revision the graph reflects, whether the
// working tree was dirty at index time, the Merkle workspace
// fingerprint that gates global-pass short-circuiting, node/edge counts
// for the index-plausibility baseline, and a JSON map of the
// per-language extractor versions that produced the graph.
//
// It is the storage half of the FreshnessFact layer; the per-file half
// lives in the Merkle leaf (the salted content hash) and the file_mtimes
// ledger.
type RepoIndexState struct {
	RepoPrefix        string
	IndexedSHA        string
	Dirty             bool
	IndexedAt         int64  // unix seconds
	WorkspaceFP       string // Merkle root at index time
	NodeCount         int
	EdgeCount         int
	ExtractorVersions string // JSON-encoded map[string]int
}

// RepoIndexStateWriter persists the freshness provenance for one repo.
// Backends without durable state simply do not implement it — the
// indexer type-asserts and skips the write when absent, exactly like the
// FileMtime ledger.
type RepoIndexStateWriter interface {
	SetRepoIndexState(state RepoIndexState) error
}

// RepoIndexStateReader reads back the freshness provenance for one repo.
// The bool is false when no state has been recorded yet (a never-indexed
// or pre-feature repo), which callers treat as "freshness unknown" — they
// never block on it.
type RepoIndexStateReader interface {
	GetRepoIndexState(repoPrefix string) (RepoIndexState, bool, error)
}

// DBStatReporter is an optional capability: report the on-disk size of the
// backing database file and its write-ahead log, in bytes. Surfaced in
// daemon_health so a runaway WAL high-water mark is observable. In-memory
// backends do not implement it.
type DBStatReporter interface {
	DBStats() (dbBytes, walBytes int64)
}
