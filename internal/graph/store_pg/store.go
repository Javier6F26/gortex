// Package store_pg is the PostgreSQL-backed implementation of graph.Store.
// It uses github.com/jackc/pgx/v5 with pgxpool for connection management
// and satisfies the same conformance suite as the in-memory and SQLite
// stores (see internal/graph/storetest).
package store_pg

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zzet/gortex/internal/graph"
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
}

var _ graph.Store = (*Store)(nil)

func Open(ctx context.Context, cfg Config) (*Store, error) {
	pool, err := cfg.openPool(ctx)
	if err != nil {
		return nil, fmt.Errorf("store_pg: open pool: %w", err)
	}
	storeCtx, cancel := context.WithCancel(context.Background())
	s := &Store{
		pool:   pool,
		config: cfg,
		ctx:    storeCtx,
		cancel: cancel,
		bundles: &bundleCache{
			fingerprints: map[string]uint64{},
			entries:      map[string]*bundleCacheEntry{},
		},
		bulk: make(map[string]*bulkState),
	}
	if err := s.ensureSchema(storeCtx); err != nil {
		pool.Close()
		cancel()
		return nil, fmt.Errorf("store_pg: ensure schema: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.cancel()
	s.pool.Close()
	return nil
}

func (s *Store) ResolveMutex() *sync.Mutex { return &s.resolveMu }
func (s *Store) NeedsRebuild() bool         { return false }

func panicOnFatal(err error) {
	if err == nil { return }
	if errors.Is(err, pgx.ErrNoRows) { return }
	panic(fmt.Errorf("store_pg: %w", err))
}

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

func (s *Store) queryNodes(ctx context.Context, q string, args ...any) []*graph.Node {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil { return nil }
	defer rows.Close()
	var out []*graph.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil { return out }
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		panicOnFatal(err)
		return nil
	}
	return out
}

func (s *Store) queryEdges(ctx context.Context, q string, args ...any) []*graph.Edge {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil { return nil }
	defer rows.Close()
	var out []*graph.Edge
	for rows.Next() {
		e, err := scanEdge(rows)
		if err != nil { return out }
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		panicOnFatal(err)
		return nil
	}
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.insertNodeLocked(s.ctx, n); err != nil { panicOnFatal(err) }
}

func (s *Store) AddBatch(nodes []*graph.Node, edges []*graph.Edge) {
	if len(nodes) == 0 && len(edges) == 0 { return }
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.insertEdgeLocked(s.ctx, e); err != nil { panicOnFatal(err) }
}

func (s *Store) GetNode(id string) *graph.Node {
	n, err := scanNode(s.pool.QueryRow(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE id = $1`, id))
	if err != nil { panicOnFatal(err); return nil }
	return n
}

func (s *Store) GetNodeByQualName(qualName string) *graph.Node {
	if qualName == "" { return nil }
	n, err := scanNode(s.pool.QueryRow(s.ctx, `SELECT `+nodeCols+` FROM nodes WHERE qual_name = $1 LIMIT 1`, qualName))
	if err != nil { panicOnFatal(err); return nil }
	return n
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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.pool.Exec(s.ctx, `DELETE FROM edges WHERE from_id=$1 AND to_id=$2 AND kind=$3`, from, to, string(kind))
	if err != nil { panicOnFatal(err); return false }
	return res.RowsAffected() > 0
}

func (s *Store) EvictFile(filePath string) (int, int) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.evictByScopeLocked(s.ctx, `SELECT id FROM nodes WHERE file_path = $1`, `DELETE FROM nodes WHERE file_path = $1`, filePath)
}

func (s *Store) EvictRepo(repoPrefix string) (int, int) {
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
	var n int
	if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM nodes`).Scan(&n); err != nil { panicOnFatal(err); return 0 }
	return n
}

func (s *Store) EdgeCount() int {
	var n int
	if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM edges`).Scan(&n); err != nil { panicOnFatal(err); return 0 }
	return n
}

func (s *Store) Stats() graph.GraphStats {
	st := graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}}
	st.TotalNodes = s.NodeCount()
	st.TotalEdges = s.EdgeCount()
	rows, err := s.pool.Query(s.ctx, `SELECT kind, COUNT(*) FROM nodes GROUP BY kind`)
	if err != nil { panicOnFatal(err); return st }
	for rows.Next() {
		var kind string; var n int
		if err := rows.Scan(&kind, &n); err != nil { rows.Close(); panicOnFatal(err); return st }
		st.ByKind[kind] = n
	}
	rows.Close()
	rows, err = s.pool.Query(s.ctx, `SELECT language, COUNT(*) FROM nodes GROUP BY language`)
	if err != nil { panicOnFatal(err); return st }
	for rows.Next() {
		var lang string; var n int
		if err := rows.Scan(&lang, &n); err != nil { rows.Close(); panicOnFatal(err); return st }
		st.ByLanguage[lang] = n
	}
	rows.Close()
	return st
}

func (s *Store) RepoStats() map[string]graph.GraphStats {
	out := map[string]graph.GraphStats{}
	rows, err := s.pool.Query(s.ctx, `SELECT repo_prefix, kind, language, COUNT(*) FROM nodes WHERE repo_prefix<>'' GROUP BY repo_prefix, kind, language`)
	if err != nil { panicOnFatal(err); return out }
	for rows.Next() {
		var repo, kind, lang string; var n int
		if err := rows.Scan(&repo, &kind, &lang, &n); err != nil { rows.Close(); panicOnFatal(err); return out }
		st, ok := out[repo]
		if !ok { st = graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}} }
		st.TotalNodes += n
		st.ByKind[kind] += n
		st.ByLanguage[lang] += n
		out[repo] = st
	}
	rows.Close()
	rows, err = s.pool.Query(s.ctx, `SELECT n.repo_prefix, COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix<>'' GROUP BY n.repo_prefix`)
	if err != nil { panicOnFatal(err); return out }
	for rows.Next() {
		var repo string; var n int
		if err := rows.Scan(&repo, &n); err != nil { rows.Close(); panicOnFatal(err); return out }
		st, ok := out[repo]
		if !ok { st = graph.GraphStats{ByKind: map[string]int{}, ByLanguage: map[string]int{}} }
		st.TotalEdges = n
		out[repo] = st
	}
	rows.Close()
	return out
}

func (s *Store) RepoPrefixes() []string {
	rows, err := s.pool.Query(s.ctx, `SELECT DISTINCT repo_prefix FROM nodes WHERE repo_prefix<>''`)
	if err != nil { panicOnFatal(err); return nil }
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil { panicOnFatal(err); return out }
		out = append(out, p)
	}
	return out
}

func (s *Store) EdgeIdentityRevisions() int { return int(s.edgeIdentityRevs.Load()) }
func (s *Store) VerifyEdgeIdentities() error { return nil }

const perNodeByteEstimate = 256
const perEdgeByteEstimate = 128

func (s *Store) RepoMemoryEstimate(repoPrefix string) graph.RepoMemoryEstimate {
	var est graph.RepoMemoryEstimate
	var n, e int
	if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM nodes WHERE repo_prefix=$1`, repoPrefix).Scan(&n); err != nil { panicOnFatal(err); return est }
	if err := s.pool.QueryRow(s.ctx, `SELECT COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix=$1`, repoPrefix).Scan(&e); err != nil { panicOnFatal(err); return est }
	est.NodeCount = n; est.EdgeCount = e
	est.NodeBytes = uint64(n) * perNodeByteEstimate
	est.EdgeBytes = uint64(e) * perEdgeByteEstimate
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
	rows, err := s.pool.Query(s.ctx, `SELECT repo_prefix, COUNT(*) FROM nodes WHERE repo_prefix<>'' GROUP BY repo_prefix`)
	if err != nil { panicOnFatal(err); return out }
	for rows.Next() {
		var repo string; var n int
		if err := rows.Scan(&repo, &n); err != nil { rows.Close(); panicOnFatal(err); return out }
		est := out[repo]; est.NodeCount = n; est.NodeBytes = uint64(n) * perNodeByteEstimate; out[repo] = est
	}
	rows.Close()
	rows, err = s.pool.Query(s.ctx, `SELECT n.repo_prefix, COUNT(*) FROM edges e JOIN nodes n ON n.id=e.from_id WHERE n.repo_prefix<>'' GROUP BY n.repo_prefix`)
	if err != nil { panicOnFatal(err); return out }
	for rows.Next() {
		var repo string; var n int
		if err := rows.Scan(&repo, &n); err != nil { rows.Close(); panicOnFatal(err); return out }
		est := out[repo]; est.EdgeCount = n; est.EdgeBytes = uint64(n) * perEdgeByteEstimate; out[repo] = est
	}
	rows.Close()
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
