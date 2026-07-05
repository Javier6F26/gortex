package store_pg

import (
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/zzet/gortex/internal/graph"
)

// This file implements graph.BulkLoader on the PostgreSQL backend using
// COPY FROM into staging tables.
//
// Design:
//
//   - BeginBulkLoad enters bulk mode. In bulk mode, AddBatch buffers rows
//     in memory instead of issuing individual INSERT statements.
//
//   - FlushBulk commits everything using COPY FROM into UNLOGGED staging
//     tables, builds indexes, then swaps tables atomically via ALTER TABLE
//     RENAME.
//
// For incremental writes outside bulk mode, standard INSERT with
// ON CONFLICT is used (the normal AddBatch path in store.go).

// Compile-time assertion: *Store satisfies graph.BulkLoader.
var _ graph.BulkLoader = (*Store)(nil)

// bulkState holds buffers accumulated during the bulk-load bracket.
// Stored per-Store (s.bulk) so multiple Store instances in the same
// process do not share state.
type bulkState struct {
	nodes []*pgNodeRow
	edges []*pgEdgeRow
}

type pgNodeRow struct {
	id           string
	kind         string
	name         string
	qualName     string
	filePath     string
	startLine    int
	endLine      int
	startCol     int
	endCol       int
	language     string
	repoPrefix   string
	workspaceID  string
	projectID    string
	sig          *string
	vis          *string
	doc          *string
	external     *int64
	returnType   *string
	isAsync      *int64
	isStatic     *int64
	isAbstract   *int64
	isExported   *int64
	updatedAt    *int64
	dataClass    *string
	metaJSON     []byte
}

type pgEdgeRow struct {
	fromID          string
	toID            string
	kind            string
	filePath        string
	line            int
	confidence      float64
	confidenceLabel string
	origin          string
	tier            string
	crossRepo       bool
	metaJSON        []byte
}

// BeginBulkLoad enters bulk-load mode. Subsequent AddBatch calls buffer
// rows instead of writing to the database.
func (s *Store) BeginBulkLoad() {
	s.bulk = &bulkState{}
}

// FlushBulk commits all buffered rows via COPY FROM into UNLOGGED staging
// tables, swaps atomically, and restores normal write mode.
func (s *Store) FlushBulk() error {
	if s.bulk == nil {
		return fmt.Errorf("store_pg: FlushBulk without BeginBulkLoad")
	}
	defer func() { s.bulk = nil }()

	if len(s.bulk.nodes) == 0 && len(s.bulk.edges) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	ctx := s.ctx

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store_pg: bulk begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// Set session-level bulk-load config.
	if _, err := tx.Exec(ctx, `SET LOCAL synchronous_commit TO OFF`); err != nil {
		return fmt.Errorf("store_pg: bulk set synchronous_commit: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL maintenance_work_mem TO '1GB'`); err != nil {
		return fmt.Errorf("store_pg: bulk set maintenance_work_mem: %w", err)
	}

	// Create UNLOGGED staging tables for nodes and edges.
	if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE nodes_bulk (LIKE nodes INCLUDING ALL)`); err != nil {
		return fmt.Errorf("store_pg: bulk create nodes_bulk: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE edges_bulk (LIKE edges INCLUDING ALL)`); err != nil {
		return fmt.Errorf("store_pg: bulk create edges_bulk: %w", err)
	}

	// COPY FROM for nodes.
	copyCols := nodeInsertCols
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"nodes_bulk"},
		parseCopyCols(copyCols),
		pgx.CopyFromSlice(len(s.bulk.nodes), func(i int) ([]any, error) {
			n := s.bulk.nodes[i]
			return []any{
				n.id, n.kind, n.name, n.qualName, n.filePath,
				n.startLine, n.endLine, n.startCol, n.endCol, n.language,
				n.repoPrefix, n.workspaceID, n.projectID,
				n.sig, n.vis, n.doc, n.external, n.returnType,
				n.isAsync, n.isStatic, n.isAbstract, n.isExported, n.updatedAt,
				n.dataClass, n.metaJSON,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("store_pg: bulk copy nodes: %w", err)
	}

	// COPY FROM for edges.
	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{"edges_bulk"},
		parseCopyCols("from_id, to_id, kind, file_path, line, confidence, confidence_label, origin, tier, cross_repo, meta"),
		pgx.CopyFromSlice(len(s.bulk.edges), func(i int) ([]any, error) {
			e := s.bulk.edges[i]
			return []any{
				e.fromID, e.toID, e.kind, e.filePath, e.line,
				e.confidence, e.confidenceLabel, e.origin, e.tier,
				e.crossRepo, e.metaJSON,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("store_pg: bulk copy edges: %w", err)
	}

	// Build indexes on staging tables.
	indexDDL := []string{
		`CREATE INDEX ON nodes_bulk(name)`,
		`CREATE INDEX ON nodes_bulk(kind)`,
		`CREATE INDEX ON nodes_bulk(file_path)`,
		`CREATE INDEX ON nodes_bulk(repo_prefix) WHERE repo_prefix <> ''`,
		`CREATE UNIQUE INDEX ON nodes_bulk(qual_name) WHERE qual_name <> ''`,
		`CREATE INDEX ON nodes_bulk USING GIN (name gin_trgm_ops)`,
		`CREATE INDEX ON edges_bulk(from_id, kind)`,
		`CREATE INDEX ON edges_bulk(to_id)`,
		`CREATE INDEX ON edges_bulk(kind)`,
	}
	for _, ddl := range indexDDL {
		if _, err := tx.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("store_pg: bulk create index: %w (DDL: %s)", err, ddl)
		}
	}

	// Swap staging tables into place.
	if _, err := tx.Exec(ctx, `ALTER TABLE nodes RENAME TO nodes_old`); err != nil {
		return fmt.Errorf("store_pg: bulk rename nodes old: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE nodes_bulk RENAME TO nodes`); err != nil {
		return fmt.Errorf("store_pg: bulk rename nodes new: %w", err)
	}
	if _, err := tx.Exec(ctx, `DROP TABLE nodes_old`); err != nil {
		return fmt.Errorf("store_pg: bulk drop nodes_old: %w", err)
	}

	if _, err := tx.Exec(ctx, `ALTER TABLE edges RENAME TO edges_old`); err != nil {
		return fmt.Errorf("store_pg: bulk rename edges old: %w", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE edges_bulk RENAME TO edges`); err != nil {
		return fmt.Errorf("store_pg: bulk rename edges new: %w", err)
	}
	if _, err := tx.Exec(ctx, `DROP TABLE edges_old`); err != nil {
		return fmt.Errorf("store_pg: bulk drop edges_old: %w", err)
	}

	// Populate content_fts and other sidecars — they are not swapped but
	// will be populated by their normal AppendContent / BulkSet* paths after
	// this bulk phase completes.

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store_pg: bulk commit: %w", err)
	}

	return nil
}

// AddBatch in bulk mode buffers rows instead of writing to the database.
// Outside bulk mode, it delegates to the normal Store.AddBatch.
func (s *Store) AddBatchBulk(nodes []*graph.Node, edges []*graph.Edge) {
	if s.bulk == nil {
		// Not in bulk mode — use the normal path.
		s.AddBatch(nodes, edges)
		return
	}

	for _, n := range nodes {
		if n == nil || n.ID == "" || graph.IsProxyNode(n) {
			continue
		}
		p, blobMeta := extractPromotedMeta(n.Meta)
		metaBlob, _ := encodeMeta(blobMeta)
		s.bulk.nodes = append(s.bulk.nodes, &pgNodeRow{
			id: n.ID, kind: string(n.Kind), name: n.Name,
			qualName: n.QualName, filePath: n.FilePath,
			startLine: n.StartLine, endLine: n.EndLine,
			startCol: n.StartColumn, endCol: n.EndColumn,
			language: n.Language, repoPrefix: n.RepoPrefix,
			workspaceID: n.WorkspaceID, projectID: n.ProjectID,
			sig: p.sig, vis: p.vis, doc: p.doc, external: p.external,
			returnType: p.returnType, isAsync: p.isAsync,
			isStatic: p.isStatic, isAbstract: p.isAbstract,
			isExported: p.isExported, updatedAt: p.updatedAt,
			dataClass: p.dataClass, metaJSON: metaBlob,
		})
	}
	for _, e := range edges {
		if e == nil || graph.IsProxyID(e.From) || graph.IsProxyID(e.To) {
			continue
		}
		metaBlob, _ := encodeMeta(e.Meta)
		crossRepo := e.CrossRepo
		s.bulk.edges = append(s.bulk.edges, &pgEdgeRow{
			fromID: e.From, toID: e.To, kind: string(e.Kind),
			filePath: e.FilePath, line: e.Line,
			confidence: e.Confidence, confidenceLabel: e.ConfidenceLabel,
			origin: e.Origin, tier: e.Tier,
			crossRepo: crossRepo, metaJSON: metaBlob,
		})
	}
}

func parseCopyCols(cols string) []string {
	// Simple parser: split by comma and trim spaces.
	out := make([]string, 0)
	start := 0
	for i := 0; i <= len(cols); i++ {
		if i == len(cols) || cols[i] == ',' {
			col := cols[start:i]
			// Trim spaces
			for len(col) > 0 && col[0] == ' ' {
				col = col[1:]
			}
			for len(col) > 0 && col[len(col)-1] == ' ' {
				col = col[:len(col)-1]
			}
			if col != "" {
				out = append(out, col)
			}
			start = i + 1
		}
	}
	return out
}
