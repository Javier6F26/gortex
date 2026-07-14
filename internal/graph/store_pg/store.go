// Package store_pg is the PostgreSQL-backed implementation of graph.Store.
// It uses github.com/jackc/pgx/v5 with pgxpool for connection management
// and satisfies the same conformance suite as the in-memory and SQLite
// stores (see internal/graph/storetest).
package store_pg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zzet/gortex/internal/graph"
	"go.uber.org/zap"
)

type Store struct {
	pool   *pgxpool.Pool
	config Config
	writeMu   sync.Mutex
	resolveMu sync.Mutex
	edgeIdentityRevs atomic.Int64
	ctx    context.Context
	cancel context.CancelFunc
	memEstMu  sync.Mutex
	memEstVal map[string]graph.RepoMemoryEstimate
	memEstAt  time.Time
	bundles *bundleCache
	// bulk holds per-repo bulk-load state, keyed by repo prefix.
	// Each entry is non-nil only between BeginBulkLoad and FlushBulk
	// for that repo. The map itself is safe for concurrent access
	// because all reads/writes happen under writeMu.
	bulk map[string]*bulkState

	// readOnly refuses every mutating method (see refuseWrite). Set from
	// Config.ReadOnly at Open.
	readOnly bool
	// logger receives WARN logs for degraded reads and refused writes.
	// Never nil after Open (defaults to zap.NewNop()).
	logger *zap.Logger

	// Health counters. degradedReads and writeRefusals are atomic;
	// lastErr/lastErrAt are guarded by healthMu.
	degradedReads atomic.Int64
	writeRefusals atomic.Int64
	healthMu      sync.Mutex
	lastErr       string
	lastErrAt     time.Time

	// lockConn holds the dedicated pooled connection that owns the writer
	// advisory lock (see writer_lock.go); nil unless AcquireWriterLock
	// succeeded. lockKey is the advisory key held on it.
	lockConn *pgxpool.Conn
	lockKey  int64
}

var _ graph.Store = (*Store)(nil)

func Open(ctx context.Context, cfg Config) (*Store, error) {
	pool, err := cfg.openPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("store_pg: open pool: %w", err)
	}
	storeCtx, cancel := context.WithCancel(context.Background())
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	s := &Store{
		pool:   pool,
		config: cfg,
		ctx:    storeCtx,
		cancel: cancel,
		bundles: &bundleCache{
			fingerprints: map[string]uint64{},
			entries:      map[string]*bundleCacheEntry{},
		},
		bulk:     make(map[string]*bulkState),
		readOnly: cfg.ReadOnly,
		logger:   logger,
	}
	if err := s.ensureSchema(storeCtx); err != nil {
		pool.Close()
		cancel()
		return nil, fmt.Errorf("store_pg: ensure schema: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.releaseWriterLock()
	s.cancel()
	s.pool.Close()
	return nil
}

func (s *Store) ResolveMutex() *sync.Mutex { return &s.resolveMu }
func (s *Store) NeedsRebuild() bool         { return false }

// panicOnFatal is a programming-error guard for WRITE paths only. Writes
// stay fail-fast: a write error other than pgx.ErrNoRows crashes the
// process rather than silently losing data. Read paths never call it —
// they route through withReadRetry (retry → degrade), so a transient PG
// error can never panic a reader. Any surviving call site is therefore a
// deliberate fail-fast on a mutation.
func panicOnFatal(err error) {
	if err == nil { return }
	if errors.Is(err, pgx.ErrNoRows) { return }
	panic(fmt.Errorf("store_pg: %w", err))
}

// ErrReadOnlyStore is returned by mutating methods (those with an error
// return) when the store was opened with Config.ReadOnly. Methods without
// an error return log-and-drop and record the refusal in health state.
var ErrReadOnlyStore = errors.New("store_pg: read-only store")

// readRetryBackoff is the per-attempt sleep before retrying a transient
// read failure. Its length is the attempt budget.
var readRetryBackoff = []time.Duration{50 * time.Millisecond, 150 * time.Millisecond, 450 * time.Millisecond}

// retryableSQLState reports whether err is a transient PostgreSQL failure
// worth retrying on a read path: connection-exception class (08),
// admin/crash shutdowns and cannot-connect-now (57P01/57P02/57P03),
// serialization_failure (40001, the SQLSTATE of a standby recovery
// conflict), deadlock_detected (40P01), query_canceled (57014, recovery
// conflict cancellation), and lock_not_available (55P03, a lock_timeout
// expiry). Broken/reset connections that surface as driver or network
// errors (failover, a killed pool connection) are also retryable.
// Caller cancellation (context.Canceled/DeadlineExceeded) is never
// retried.
func retryableSQLState(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		code := pgErr.Code
		switch {
		case strings.HasPrefix(code, "08"): // connection_exception class
			return true
		case code == "57P01", code == "57P02", code == "57P03": // shutdown / cannot_connect_now
			return true
		case code == "40001": // serialization_failure (standby recovery conflict)
			return true
		case code == "40P01": // deadlock_detected
			return true
		case code == "57014": // query_canceled (recovery conflict cancellation)
			return true
		case code == "55P03": // lock_not_available (lock_timeout)
			return true
		default:
			return false
		}
	}
	// Not a PgError: a broken/reset connection (failover, pool conn killed
	// mid-iteration) surfaces as a driver or network error. These are
	// transient and safe to retry for reads.
	return pgconn.SafeToRetry(err) || isNetworkError(err)
}

func isNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	return false
}

// withReadRetry runs fn, retrying on transient PostgreSQL errors with
// bounded exponential backoff. fn must be idempotent (it resets and
// re-populates its output on every attempt). On success it returns
// immediately. On a non-retryable error, or after the attempt budget is
// exhausted, it records a degraded read (WARN log + health counter) and
// returns — leaving the caller's output at its zero value. It never
// panics: read paths degrade rather than crash the process.
func (s *Store) withReadRetry(tag string, fn func() error) {
	var err error
	for attempt := 0; attempt < len(readRetryBackoff); attempt++ {
		if err = fn(); err == nil {
			return
		}
		if !retryableSQLState(err) {
			break
		}
		if attempt == len(readRetryBackoff)-1 {
			break
		}
		select {
		case <-s.ctx.Done():
			s.recordDegradedRead(tag, err)
			return
		case <-time.After(readRetryBackoff[attempt]):
		}
	}
	s.recordDegradedRead(tag, err)
}

// recordDegradedRead increments the degraded-read counter, stores the
// error as the last-error, and logs at WARN.
func (s *Store) recordDegradedRead(tag string, err error) {
	if err == nil {
		return
	}
	s.degradedReads.Add(1)
	s.healthMu.Lock()
	s.lastErr = err.Error()
	s.lastErrAt = time.Now()
	s.healthMu.Unlock()
	s.logger.Warn("store_pg: degraded read", zap.String("query", tag), zap.Error(err))
}

// refuseWrite reports whether the store is read-only. When true it records
// the refusal in health state and logs at WARN. Every mutating method
// calls it first and short-circuits on true (returning ErrReadOnlyStore
// or the appropriate zero value).
func (s *Store) refuseWrite(method string) bool {
	if !s.readOnly {
		return false
	}
	s.writeRefusals.Add(1)
	s.healthMu.Lock()
	s.lastErr = "read-only store: refused " + method
	s.lastErrAt = time.Now()
	s.healthMu.Unlock()
	s.logger.Warn("store_pg: write refused on read-only store", zap.String("method", method))
	return true
}

// Health reports the store's degraded-operation counters. Satisfies
// graph.StoreHealthReporter and is surfaced in daemon_health.
func (s *Store) Health() graph.StoreHealth {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	var unix int64
	if !s.lastErrAt.IsZero() {
		unix = s.lastErrAt.Unix()
	}
	return graph.StoreHealth{
		DegradedReads: s.degradedReads.Load(),
		WriteRefusals: s.writeRefusals.Load(),
		LastError:     s.lastErr,
		LastErrorUnix: unix,
	}
}

var _ graph.StoreHealthReporter = (*Store)(nil)

func minInt(a, b int) int {
	if a < b { return a }
	return b
}

const lookupChunkSize = 5000
const reindexChunkSize = 5000

const nodeCols = `id, kind, name, qual_name, file_path, start_line, end_line, start_column, end_column, language, repo_prefix, workspace_id, project_id, signature, visibility, doc, external, return_type, is_async, is_static, is_abstract, is_exported, updated_at, data_class, meta`

const edgeCols = `from_id, to_id, kind, file_path, line, confidence, confidence_label, origin, tier, cross_repo, meta`
const edgeColsQualified = `e.from_id, e.to_id, e.kind, e.file_path, e.line, e.confidence, e.confidence_label, e.origin, e.tier, e.cross_repo, e.meta`

func scanNode(row pgx.Row) (*graph.Node, error) {
	var n graph.Node
	var metaBlob []byte
	var p promotedNodeMeta
	err := row.Scan(&n.ID, &n.Kind, &n.Name, &n.QualName, &n.FilePath, &n.StartLine, &n.EndLine, &n.StartColumn, &n.EndColumn, &n.Language, &n.RepoPrefix, &n.WorkspaceID, &n.ProjectID, &p.sig, &p.vis, &p.doc, &p.external, &p.returnType, &p.isAsync, &p.isStatic, &p.isAbstract, &p.isExported, &p.updatedAt, &p.dataClass, &metaBlob)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return nil, nil }
		return nil, err
	}
	if len(metaBlob) > 0 {
		m, derr := decodeMeta(metaBlob)
		if derr != nil { return nil, derr }
		n.Meta = m
	}
	restorePromotedMeta(&n, p)
	return &n, nil
}

func scanEdge(row pgx.Row) (*graph.Edge, error) {
	var e graph.Edge
	var metaBlob []byte
	var crossRepo bool
	err := row.Scan(&e.From, &e.To, &e.Kind, &e.FilePath, &e.Line, &e.Confidence, &e.ConfidenceLabel, &e.Origin, &e.Tier, &crossRepo, &metaBlob)
	e.CrossRepo = crossRepo
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return nil, nil }
		return nil, err
	}
	if len(metaBlob) > 0 {
		m, derr := decodeMeta(metaBlob)
		if derr != nil { return nil, derr }
		e.Meta = m
	}
	return &e, nil
}

// queryNodes runs q and materializes the result. Both failure points —
// the pool.Query error and rows.Err() after iteration — route through the
// same retry-then-degrade path, so a transient fault behaves identically
// regardless of where it surfaces (ending the former silent-nil/panic
// asymmetry). On retry exhaustion it returns nil.
func (s *Store) queryNodes(ctx context.Context, q string, args ...any) []*graph.Node {
	var out []*graph.Node
	s.withReadRetry("queryNodes", func() error {
		out = nil
		rows, err := s.pool.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var acc []*graph.Node
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				return err
			}
			acc = append(acc, n)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = acc
		return nil
	})
	return out
}

// queryEdges is the edge analogue of queryNodes; see its doc for the
// uniform retry-then-degrade behavior across both failure points.
func (s *Store) queryEdges(ctx context.Context, q string, args ...any) []*graph.Edge {
	var out []*graph.Edge
	s.withReadRetry("queryEdges", func() error {
		out = nil
		rows, err := s.pool.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var acc []*graph.Edge
		for rows.Next() {
			e, err := scanEdge(rows)
			if err != nil {
				return err
			}
			acc = append(acc, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = acc
		return nil
	})
	return out
}

const nodeInsertCols = `id, kind, name, qual_name, file_path, start_line, end_line, start_column, end_column, language, repo_prefix, workspace_id, project_id, signature, visibility, doc, external, return_type, is_async, is_static, is_abstract, is_exported, updated_at, data_class, meta`

const nodeInsertConflict = `ON CONFLICT (id) DO UPDATE SET kind=EXCLUDED.kind, name=EXCLUDED.name, qual_name=EXCLUDED.qual_name, file_path=EXCLUDED.file_path, start_line=EXCLUDED.start_line, end_line=EXCLUDED.end_line, start_column=EXCLUDED.start_column, end_column=EXCLUDED.end_column, language=EXCLUDED.language, repo_prefix=EXCLUDED.repo_prefix, workspace_id=EXCLUDED.workspace_id, project_id=EXCLUDED.project_id, signature=EXCLUDED.signature, visibility=EXCLUDED.visibility, doc=EXCLUDED.doc, external=EXCLUDED.external, return_type=EXCLUDED.return_type, is_async=EXCLUDED.is_async, is_static=EXCLUDED.is_static, is_abstract=EXCLUDED.is_abstract, is_exported=EXCLUDED.is_exported, updated_at=EXCLUDED.updated_at, data_class=EXCLUDED.data_class, meta=EXCLUDED.meta`

func (s *Store) insertNodeLocked(ctx context.Context, n *graph.Node) error {
	if n == nil || n.ID == "" { return nil }
	if graph.IsProxyNode(n) { return nil }
	p, blobMeta := extractPromotedMeta(n.Meta)
	metaBlob, err := encodeMeta(blobMeta)
	if err != nil { return err }
	_, err = s.pool.Exec(ctx, `INSERT INTO nodes (`+nodeInsertCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25) `+nodeInsertConflict,
		n.ID, string(n.Kind), n.Name, n.QualName, n.FilePath,
		n.StartLine, n.EndLine, n.StartColumn, n.EndColumn, n.Language,
		n.RepoPrefix, n.WorkspaceID, n.ProjectID,
		p.sig, p.vis, p.doc, p.external, p.returnType,
		p.isAsync, p.isStatic, p.isAbstract, p.isExported, p.updatedAt,
		p.dataClass, metaBlob)
	return err
}

const edgeInsertCols = `from_id, to_id, kind, file_path, line, confidence, confidence_label, origin, tier, cross_repo, meta`

func (s *Store) insertEdgeLocked(ctx context.Context, e *graph.Edge) error {
	if e == nil { return nil }
	if graph.IsProxyID(e.From) || graph.IsProxyID(e.To) { return nil }
	metaBlob, err := encodeMeta(e.Meta)
	if err != nil { return err }
	_, err = s.pool.Exec(ctx, `INSERT INTO edges (`+edgeInsertCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`,
		e.From, e.To, string(e.Kind), e.FilePath, e.Line,
		e.Confidence, e.ConfidenceLabel, e.Origin, e.Tier,
		e.CrossRepo, metaBlob)
	return err
}

func (s *Store) AddNode(n *graph.Node) {
	if s.refuseWrite("AddNode") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.insertNodeLocked(s.ctx, n); err != nil { panicOnFatal(err) }
}

func (s *Store) AddBatch(nodes []*graph.Node, edges []*graph.Edge) {
	if len(nodes) == 0 && len(edges) == 0 { return }
	if s.refuseWrite("AddBatch") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// In bulk mode, buffer rows in memory for later FlushBulk via COPY FROM.
	if bs := s.bulkForBatch(nodes, edges); bs != nil {
		s.bufferBatchLocked(bs, nodes, edges)
		return
	}

	ctx := s.ctx
	tx, err := s.pool.Begin(ctx)
	if err != nil { panicOnFatal(err); return }
	commit := false
	defer func() { if !commit { _ = tx.Rollback(ctx) } }()
	for _, n := range nodes {
		if n == nil || n.ID == "" { continue }
		if graph.IsProxyNode(n) { continue }
		p, blobMeta := extractPromotedMeta(n.Meta)
		metaBlob, err := encodeMeta(blobMeta)
		if err != nil { panicOnFatal(err); return }
		if _, err := tx.Exec(ctx, `INSERT INTO nodes (`+nodeInsertCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25) `+nodeInsertConflict,
			n.ID, string(n.Kind), n.Name, n.QualName, n.FilePath,
			n.StartLine, n.EndLine, n.StartColumn, n.EndColumn, n.Language,
			n.RepoPrefix, n.WorkspaceID, n.ProjectID,
			p.sig, p.vis, p.doc, p.external, p.returnType,
			p.isAsync, p.isStatic, p.isAbstract, p.isExported, p.updatedAt,
			p.dataClass, metaBlob); err != nil { panicOnFatal(err); return }
	}
	for _, e := range edges {
		if e == nil { continue }
		if graph.IsProxyID(e.From) || graph.IsProxyID(e.To) { continue }
		metaBlob, err := encodeMeta(e.Meta)
		if err != nil { panicOnFatal(err); return }
		if _, err := tx.Exec(ctx, `INSERT INTO edges (`+edgeInsertCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`,
			e.From, e.To, string(e.Kind), e.FilePath, e.Line,
			e.Confidence, e.ConfidenceLabel, e.Origin, e.Tier,
			e.CrossRepo, metaBlob); err != nil { panicOnFatal(err); return }
	}
	if err := tx.Commit(ctx); err != nil { panicOnFatal(err); return }
	commit = true
}

func (s *Store) AddEdge(e *graph.Edge) {
	if e == nil { return }
	if s.refuseWrite("AddEdge") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.insertEdgeLocked(s.ctx, e); err != nil { panicOnFatal(err) }
}

func (s *Store) GetNode(id string) *graph.Node {
	var out *graph.Node
	s.withReadRetry("GetNode", func() error {
		n, err := scanNode(s.pool.QueryRow(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = $1`, id))
		if err != nil { return err }
		out = n
		return nil
	})
	return out
}

func (s *Store) GetNodeByQualName(qualName string) *graph.Node {
	if qualName == "" { return nil }
	var out *graph.Node
	s.withReadRetry("GetNodeByQualName", func() error {
		n, err := scanNode(s.pool.QueryRow(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE qual_name = $1 LIMIT 1`, qualName))
		if err != nil { return err }
		out = n
		return nil
	})
	return out
}

func (s *Store) FindNodesByName(name string) []*graph.Node {
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE name = $1`, name)
}

func (s *Store) FindNodesByNameInRepo(name, repoPrefix string) []*graph.Node {
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE name = $1 AND repo_prefix = $2`, name, repoPrefix)
}

func (s *Store) FindNodesByNameContaining(substr string, limit int) []*graph.Node {
	if substr == "" { return nil }
	q := `SELECT ` + nodeCols + ` FROM nodes WHERE name ILIKE '%' || $1 || '%' ORDER BY id`
	if limit > 0 { return s.queryNodes(s.ctx, q+` LIMIT $2`, substr, limit) }
	return s.queryNodes(s.ctx, q, substr)
}

func (s *Store) GetFileNodes(filePath string) []*graph.Node {
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE file_path = $1`, filePath)
}

func (s *Store) GetRepoNodes(repoPrefix string) []*graph.Node {
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE repo_prefix = $1`, repoPrefix)
}

func (s *Store) AllNodes() []*graph.Node {
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes`)
}

func (s *Store) GetOutEdges(nodeID string) []*graph.Edge {
	return s.queryEdges(s.ctx, `SELECT `+edgeCols+` FROM edges WHERE from_id = $1`, nodeID)
}

func (s *Store) GetInEdges(nodeID string) []*graph.Edge {
	return s.queryEdges(s.ctx, `SELECT `+edgeCols+` FROM edges WHERE to_id = $1`, nodeID)
}

func (s *Store) GetRepoEdges(repoPrefix string) []*graph.Edge {
	if repoPrefix == "" { return nil }
	return s.queryEdges(s.ctx, `SELECT `+edgeColsQualified+` FROM edges e JOIN nodes n ON n.id = e.from_id WHERE n.repo_prefix = $1`, repoPrefix)
}

func (s *Store) AllEdges() []*graph.Edge {
	return s.queryEdges(s.ctx, `SELECT `+edgeCols+` FROM edges`)
}

func (s *Store) SetEdgeProvenance(e *graph.Edge, newOrigin string) bool {
	if e == nil { return false }
	if s.refuseWrite("SetEdgeProvenance") { return false }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx
	var storedOrigin string
	err := s.pool.QueryRow(ctx, `SELECT origin FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3 AND file_path=$4 AND line=$5`, e.From, e.To, string(e.Kind), e.FilePath, e.Line).Scan(&storedOrigin)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return false }
		panicOnFatal(err); return false
	}
	if storedOrigin == newOrigin { return false }
	newTier := e.Tier
	if newTier != "" { newTier = graph.ResolvedBy(newOrigin) }
	if _, err := s.pool.Exec(ctx, `UPDATE edges SET origin=$1, tier=$2 WHERE from_id=$3 AND to_id=$4 AND kind=$5 AND file_path=$6 AND line=$7`, newOrigin, newTier, e.From, e.To, string(e.Kind), e.FilePath, e.Line); err != nil {
		panicOnFatal(err); return false
	}
	e.Origin = newOrigin
	if e.Tier != "" { e.Tier = newTier }
	s.edgeIdentityRevs.Add(1)
	return true
}

func (s *Store) PersistEdgeAttributes(e *graph.Edge) {
	if e == nil { return }
	if s.refuseWrite("PersistEdgeAttributes") { return }
	metaBlob, err := encodeMeta(e.Meta)
	if err != nil { panicOnFatal(err); return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.pool.Exec(s.ctx, `UPDATE edges SET confidence=$1, confidence_label=$2, origin=$3, tier=$4, meta=$5 WHERE from_id=$6 AND to_id=$7 AND kind=$8 AND file_path=$9 AND line=$10`, e.Confidence, e.ConfidenceLabel, e.Origin, e.Tier, metaBlob, e.From, e.To, string(e.Kind), e.FilePath, e.Line); err != nil {
		panicOnFatal(err)
	}
}

var _ graph.EdgeMetaBatchPersister = (*Store)(nil)

func (s *Store) PersistEdgeAttributesBatch(edges []*graph.Edge) {
	if len(edges) == 0 { return }
	if s.refuseWrite("PersistEdgeAttributesBatch") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx
	for i := 0; i < len(edges); i += reindexChunkSize {
		end := minInt(i+reindexChunkSize, len(edges))
		chunk := edges[i:end]
		tx, err := s.pool.Begin(ctx)
		if err != nil { panicOnFatal(err); return }
		for _, e := range chunk {
			if e == nil { continue }
			metaBlob, err := encodeMeta(e.Meta)
			if err != nil { _ = tx.Rollback(ctx); panicOnFatal(err); return }
			if _, err := tx.Exec(ctx, `UPDATE edges SET confidence=$1, confidence_label=$2, origin=$3, tier=$4, meta=$5 WHERE from_id=$6 AND to_id=$7 AND kind=$8 AND file_path=$9 AND line=$10`, e.Confidence, e.ConfidenceLabel, e.Origin, e.Tier, metaBlob, e.From, e.To, string(e.Kind), e.FilePath, e.Line); err != nil {
				_ = tx.Rollback(ctx); panicOnFatal(err); return
			}
		}
		if err := tx.Commit(ctx); err != nil { panicOnFatal(err); return }
	}
}

func (s *Store) ReindexEdge(e *graph.Edge, oldTo string) {
	if e == nil || oldTo == e.To { return }
	if s.refuseWrite("ReindexEdge") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx
	if _, err := s.pool.Exec(ctx, `DELETE FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3 AND file_path=$4 AND line=$5`, e.From, oldTo, string(e.Kind), e.FilePath, e.Line); err != nil {
		panicOnFatal(err); return
	}
	if err := s.insertEdgeLocked(ctx, e); err != nil { panicOnFatal(err) }
}

func (s *Store) ReindexEdges(batch []graph.EdgeReindex) {
	if len(batch) == 0 { return }
	if s.refuseWrite("ReindexEdges") { return }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx
	for i := 0; i < len(batch); i += reindexChunkSize {
		end := minInt(i+reindexChunkSize, len(batch))
		chunk := batch[i:end]
		tx, err := s.pool.Begin(ctx)
		if err != nil { panicOnFatal(err); return }
		for _, r := range chunk {
			if r.Edge == nil || r.OldTo == r.Edge.To { continue }
			if _, err := tx.Exec(ctx, `DELETE FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3 AND file_path=$4 AND line=$5`, r.Edge.From, r.OldTo, string(r.Edge.Kind), r.Edge.FilePath, r.Edge.Line); err != nil {
				_ = tx.Rollback(ctx); panicOnFatal(err); return
			}
			metaBlob, err := encodeMeta(r.Edge.Meta)
			if err != nil { _ = tx.Rollback(ctx); panicOnFatal(err); return }
			if _, err := tx.Exec(ctx, `INSERT INTO edges (`+edgeInsertCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`,
				r.Edge.From, r.Edge.To, string(r.Edge.Kind), r.Edge.FilePath, r.Edge.Line,
				r.Edge.Confidence, r.Edge.ConfidenceLabel, r.Edge.Origin, r.Edge.Tier,
				r.Edge.CrossRepo, metaBlob); err != nil {
				_ = tx.Rollback(ctx); panicOnFatal(err); return
			}
		}
		if err := tx.Commit(ctx); err != nil { panicOnFatal(err); return }
	}
}

func (s *Store) SetEdgeProvenanceBatch(batch []graph.EdgeProvenanceUpdate) int {
	if len(batch) == 0 { return 0 }
	if s.refuseWrite("SetEdgeProvenanceBatch") { return 0 }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := s.ctx
	totalChanged := 0
	for i := 0; i < len(batch); i += reindexChunkSize {
		end := minInt(i+reindexChunkSize, len(batch))
		chunk := batch[i:end]
		tx, err := s.pool.Begin(ctx)
		if err != nil { panicOnFatal(err); return totalChanged }
		chunkChanged := 0
		for _, u := range chunk {
			if u.Edge == nil { continue }
			var storedOrigin string
			err := tx.QueryRow(ctx, `SELECT origin FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3 AND file_path=$4 AND line=$5`, u.Edge.From, u.Edge.To, string(u.Edge.Kind), u.Edge.FilePath, u.Edge.Line).Scan(&storedOrigin)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) { continue }
				_ = tx.Rollback(ctx); panicOnFatal(err); return totalChanged
			}
			if storedOrigin == u.NewOrigin { continue }
			newTier := u.Edge.Tier
			if newTier != "" { newTier = graph.ResolvedBy(u.NewOrigin) }
			if _, err := tx.Exec(ctx, `UPDATE edges SET origin=$1, tier=$2 WHERE from_id=$3 AND to_id=$4 AND kind=$5 AND file_path=$6 AND line=$7`, u.NewOrigin, newTier, u.Edge.From, u.Edge.To, string(u.Edge.Kind), u.Edge.FilePath, u.Edge.Line); err != nil {
				_ = tx.Rollback(ctx); panicOnFatal(err); return totalChanged
			}
			u.Edge.Origin = u.NewOrigin
			if u.Edge.Tier != "" { u.Edge.Tier = newTier }
			chunkChanged++
		}
		if err := tx.Commit(ctx); err != nil { panicOnFatal(err); return totalChanged }
		if chunkChanged > 0 { s.edgeIdentityRevs.Add(int64(chunkChanged)) }
		totalChanged += chunkChanged
	}
	return totalChanged
}

func (s *Store) RemoveEdge(from, to string, kind graph.EdgeKind) bool {
	if s.refuseWrite("RemoveEdge") { return false }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.pool.Exec(s.ctx, `DELETE FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3`, from, to, string(kind))
	if err != nil { panicOnFatal(err); return false }
	return res.RowsAffected() > 0
}

func (s *Store) EvictFile(filePath string) (int, int) {
	if s.refuseWrite("EvictFile") { return 0, 0 }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.evictByScopeLocked(s.ctx, `SELECT id FROM nodes WHERE file_path = $1`, `DELETE FROM nodes WHERE file_path = $1`, filePath)
}

func (s *Store) EvictRepo(repoPrefix string) (int, int) {
	if s.refuseWrite("EvictRepo") { return 0, 0 }
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.evictByScopeLocked(s.ctx, `SELECT id FROM nodes WHERE repo_prefix = $1`, `DELETE FROM nodes WHERE repo_prefix = $1`, repoPrefix)
}

func (s *Store) evictByScopeLocked(ctx context.Context, selectIDsSQL, deleteNodesSQL, scope string) (int, int) {
	rows, err := s.pool.Query(ctx, selectIDsSQL, scope)
	if err != nil { panicOnFatal(err); return 0, 0 }
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil { rows.Close(); panicOnFatal(err); return 0, 0 }
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 { return 0, 0 }
	var edgesRemoved int
	const evictEdgeChunk = 900
	for _, col := range []string{"from_id", "to_id"} {
		for start := 0; start < len(ids); start += evictEdgeChunk {
			end := minInt(start+evictEdgeChunk, len(ids))
			chunk := ids[start:end]
			placeholders := make([]string, len(chunk))
			args := make([]any, len(chunk))
			for i, id := range chunk {
				placeholders[i] = fmt.Sprintf("$%d", i+1)
				args[i] = id
			}
			res, err := s.pool.Exec(ctx, `DELETE FROM edges WHERE `+col+` IN (`+strings.Join(placeholders, ",")+`)`, args...)
			if err != nil { panicOnFatal(err); return 0, edgesRemoved }
			edgesRemoved += int(res.RowsAffected())
		}
	}
	res, err := s.pool.Exec(ctx, deleteNodesSQL, scope)
	if err != nil { panicOnFatal(err); return 0, edgesRemoved }
	return int(res.RowsAffected()), edgesRemoved
}

func (s *Store) GetNodesByIDs(ids []string) map[string]*graph.Node {
	if len(ids) == 0 { return nil }
	uniq := dedupeNonEmpty(ids)
	out := make(map[string]*graph.Node, len(uniq))
	for i := 0; i < len(uniq); i += lookupChunkSize {
		end := minInt(i+lookupChunkSize, len(uniq))
		chunk := uniq[i:end]
		for _, n := range s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = ANY($1)`, chunk) {
			if n != nil { out[n.ID] = n }
		}
	}
	return out
}

func (s *Store) FindNodesByNames(names []string) map[string][]*graph.Node {
	if len(names) == 0 { return nil }
	uniq := dedupeNonEmpty(names)
	out := make(map[string][]*graph.Node, len(uniq))
	for i := 0; i < len(uniq); i += lookupChunkSize {
		end := minInt(i+lookupChunkSize, len(uniq))
		chunk := uniq[i:end]
		for _, n := range s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE name = ANY($1)`, chunk) {
			if n == nil { continue }
			out[n.Name] = append(out[n.Name], n)
		}
	}
	return out
}

func (s *Store) GetNodesByQualNames(qualNames []string) map[string]*graph.Node {
	if len(qualNames) == 0 { return nil }
	uniq := dedupeNonEmpty(qualNames)
	out := make(map[string]*graph.Node, len(uniq))
	for i := 0; i < len(uniq); i += lookupChunkSize {
		end := minInt(i+lookupChunkSize, len(uniq))
		chunk := uniq[i:end]
		for _, n := range s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE qual_name = ANY($1)`, chunk) {
			if n == nil { continue }
			if _, ok := out[n.QualName]; !ok { out[n.QualName] = n }
		}
	}
	return out
}

func (s *Store) GetOutEdgesByNodeIDs(ids []string) map[string][]*graph.Edge {
	return s.edgesByNodeIDs(s.ctx, ids, "from_id", func(e *graph.Edge) string { return e.From })
}

func (s *Store) GetInEdgesByNodeIDs(ids []string) map[string][]*graph.Edge {
	return s.edgesByNodeIDs(s.ctx, ids, "to_id", func(e *graph.Edge) string { return e.To })
}

func (s *Store) edgesByNodeIDs(ctx context.Context, ids []string, col string, keyFn func(*graph.Edge) string) map[string][]*graph.Edge {
	out := make(map[string][]*graph.Edge, len(ids))
	if len(ids) == 0 { return out }
	uniq := dedupeNonEmpty(ids)
	for i := 0; i < len(uniq); i += lookupChunkSize {
		end := minInt(i+lookupChunkSize, len(uniq))
		chunk := uniq[i:end]
		for _, e := range s.queryEdges(ctx, `SELECT `+edgeCols+` FROM edges WHERE `+col+` = ANY($1)`, chunk) {
			k := keyFn(e)
			out[k] = append(out[k], e)
		}
	}
	return out
}

func (s *Store) NodeCount() int {
	var out int
	s.withReadRetry("NodeCount", func() error {
		var n int
		if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil { return err }
		out = n
		return nil
	})
	return out
}

func (s *Store) EdgeCount() int {
	var out int
	s.withReadRetry("EdgeCount", func() error {
		var n int
		if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM edges`).Scan(&n); err != nil { return err }
		out = n
		return nil
	})
	return out
}

func (s *Store) Stats() graph.GraphStats {
	st := graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}}
	st.TotalNodes = s.NodeCount()
	st.TotalEdges = s.EdgeCount()
	s.withReadRetry("Stats", func() error {
		byKind := map[string]int{}
		byLang := map[string]int{}
		rows, err := s.pool.Query(s.ctx, `SELECT kind, COUNT(*) FROM nodes GROUP BY kind`)
		if err != nil { return err }
		for rows.Next() {
			var kind string; var n int
			if err := rows.Scan(&kind, &n); err != nil { rows.Close(); return err }
			byKind[kind] = n
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		rows, err = s.pool.Query(s.ctx, `SELECT language, COUNT(*) FROM nodes GROUP BY language`)
		if err != nil { return err }
		for rows.Next() {
			var lang string; var n int
			if err := rows.Scan(&lang, &n); err != nil { rows.Close(); return err }
			byLang[lang] = n
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		st.ByKind = byKind
		st.ByLanguage = byLang
		return nil
	})
	return st
}

func (s *Store) RepoStats() map[string]graph.GraphStats {
	out := map[string]graph.GraphStats{}
	s.withReadRetry("RepoStats", func() error {
		acc := map[string]graph.GraphStats{}
		rows, err := s.pool.Query(s.ctx, `SELECT repo_prefix, kind, language, COUNT(*) FROM nodes WHERE repo_prefix<>'' GROUP BY repo_prefix, kind, language`)
		if err != nil { return err }
		for rows.Next() {
			var repo, kind, lang string; var n int
			if err := rows.Scan(&repo, &kind, &lang, &n); err != nil { rows.Close(); return err }
			st, ok := acc[repo]
			if !ok { st = graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}} }
			st.TotalNodes += n
			st.ByKind[kind] += n
			st.ByLanguage[lang] += n
			acc[repo] = st
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		rows, err = s.pool.Query(s.ctx, `SELECT n.repo_prefix, COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix<>'' GROUP BY n.repo_prefix`)
		if err != nil { return err }
		for rows.Next() {
			var repo string; var n int
			if err := rows.Scan(&repo, &n); err != nil { rows.Close(); return err }
			st, ok := acc[repo]
			if !ok { st = graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}} }
			st.TotalEdges = n
			acc[repo] = st
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		out = acc
		return nil
	})
	return out
}

func (s *Store) RepoPrefixes() []string {
	var out []string
	s.withReadRetry("RepoPrefixes", func() error {
		out = nil
		rows, err := s.pool.Query(s.ctx, `SELECT DISTINCT repo_prefix FROM nodes WHERE repo_prefix<>''`)
		if err != nil { return err }
		defer rows.Close()
		var acc []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil { return err }
			acc = append(acc, p)
		}
		if err := rows.Err(); err != nil { return err }
		out = acc
		return nil
	})
	return out
}

func (s *Store) EdgeIdentityRevisions() int { return int(s.edgeIdentityRevs.Load()) }
func (s *Store) VerifyEdgeIdentities() error { return nil }

const perNodeByteEstimate = 256
const perEdgeByteEstimate = 128

func (s *Store) RepoMemoryEstimate(repoPrefix string) graph.RepoMemoryEstimate {
	var est graph.RepoMemoryEstimate
	s.withReadRetry("RepoMemoryEstimate", func() error {
		var n, e int
		if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM nodes WHERE repo_prefix=$1`, repoPrefix).Scan(&n); err != nil { return err }
		if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix=$1`, repoPrefix).Scan(&e); err != nil { return err }
		est.NodeCount = n; est.EdgeCount = e
		est.NodeBytes = uint64(n) * perNodeByteEstimate
		est.EdgeBytes = uint64(e) * perEdgeByteEstimate
		return nil
	})
	return est
}

const memEstTTL = 3 * time.Second

func (s *Store) AllRepoMemoryEstimates() map[string]graph.RepoMemoryEstimate {
	s.memEstMu.Lock()
	defer s.memEstMu.Unlock()
	if s.memEstVal != nil && time.Since(s.memEstAt) < memEstTTL {
		out := make(map[string]graph.RepoMemoryEstimate, len(s.memEstVal))
		for k, v := range s.memEstVal { out[k] = v }
		return out
	}
	out := map[string]graph.RepoMemoryEstimate{}
	s.withReadRetry("AllRepoMemoryEstimates", func() error {
		acc := map[string]graph.RepoMemoryEstimate{}
		rows, err := s.pool.Query(s.ctx, `SELECT repo_prefix, COUNT(*) FROM nodes WHERE repo_prefix<>'' GROUP BY repo_prefix`)
		if err != nil { return err }
		for rows.Next() {
			var repo string; var n int
			if err := rows.Scan(&repo, &n); err != nil { rows.Close(); return err }
			est := acc[repo]; est.NodeCount = n; est.NodeBytes = uint64(n) * perNodeByteEstimate; acc[repo] = est
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		rows, err = s.pool.Query(s.ctx, `SELECT n.repo_prefix, COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix<>'' GROUP BY n.repo_prefix`)
		if err != nil { return err }
		for rows.Next() {
			var repo string; var n int
			if err := rows.Scan(&repo, &n); err != nil { rows.Close(); return err }
			est := acc[repo]; est.EdgeCount = n; est.EdgeBytes = uint64(n) * perEdgeByteEstimate; acc[repo] = est
		}
		if err := rows.Err(); err != nil { rows.Close(); return err }
		rows.Close()
		out = acc
		return nil
	})
	s.memEstVal = out; s.memEstAt = time.Now()
	result := make(map[string]graph.RepoMemoryEstimate, len(out))
	for k, v := range out { result[k] = v }
	return result
}

func (s *Store) EdgesByKind(kind graph.EdgeKind) iter.Seq[*graph.Edge] {
	return func(yield func(*graph.Edge) bool) {
		out := s.queryEdges(s.ctx, `SELECT `+edgeCols+` FROM edges WHERE kind = $1`, string(kind))
		for _, e := range out { if !yield(e) { return } }
	}
}

func (s *Store) NodesByKind(kind graph.NodeKind) iter.Seq[*graph.Node] {
	return func(yield func(*graph.Node) bool) {
		out := s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE kind = $1`, string(kind))
		for _, n := range out { if !yield(n) { return } }
	}
}

func (s *Store) EdgesWithUnresolvedTarget() iter.Seq[*graph.Edge] {
	return func(yield func(*graph.Edge) bool) {
		out := s.queryEdges(s.ctx, `SELECT `+edgeCols+` FROM edges WHERE to_id LIKE 'unresolved::%' OR to_id LIKE '%::unresolved::%'`)
		for _, e := range out { if !yield(e) { return } }
	}
}

func dedupeNonEmpty(in []string) []string {
	if len(in) == 0 { return nil }
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" { continue }
		if _, ok := seen[s]; ok { continue }
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

var _ = runtime.NumCPU

func (s *Store) GetRepoNonContentNodes(repoPrefix string) []*graph.Node {
	filter := `COALESCE(data_class, meta->>'data_class') IS DISTINCT FROM 'content'`
	if repoPrefix == "" { return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE `+filter) }
	return s.queryNodes(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE repo_prefix = $1 AND `+filter, repoPrefix)
}
