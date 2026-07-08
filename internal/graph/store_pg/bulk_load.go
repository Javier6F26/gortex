package store_pg

import (
	"context"
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
	nodes      []*pgNodeRow
	edges      []*pgEdgeRow
	tableEmpty bool // true when the nodes table was empty at BeginBulkLoad time
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
//
// Always activates bulk mode (no empty-table gate). The table-empty flag
// captured here tells FlushBulk whether a destructive table swap is safe
// (true) or a non-destructive merge is required (false).
func (s *Store) BeginBulkLoad() {
	// Re-entrancy guard: a second BeginBulkLoad without an intervening
	// FlushBulk is a no-op.
	if s.bulk != nil {
		return
	}

	// Capture whether the nodes table is empty so FlushBulk can choose
	// between the destructive swap (safe on empty) and the non-destructive
	// merge (required when data from prior repos exists).
	var empty bool
	if err := s.pool.QueryRow(s.ctx,
		`SELECT NOT EXISTS (SELECT 1 FROM nodes LIMIT 1)`,
	).Scan(&empty); err != nil {
		// Non-fatal: default to non-destructive path on query failure.
		empty = false
	}

	s.bulk = &bulkState{
		nodes:      make([]*pgNodeRow, 0, 100000),
		edges:      make([]*pgEdgeRow, 0, 100000),
		tableEmpty: empty,
	}
}

// FlushBulk commits all buffered rows via COPY FROM into UNLOGGED staging
// tables and restores normal write mode.
//
// Routing:
//   - tableEmpty (first repo, empty store): destructive table swap
//     (UNLOGGED staging → COPY FROM → build indexes → swap → drop old)
//   - !tableEmpty (repos 2+, warm restart): non-destructive merge
//     (UNLOGGED staging → COPY FROM → INSERT INTO SELECT → drop staging)
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

	// Set session-level bulk-load config for both paths.
	if _, err := tx.Exec(ctx, `SET LOCAL synchronous_commit TO OFF`); err != nil {
		return fmt.Errorf("store_pg: bulk set synchronous_commit: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL maintenance_work_mem TO '1GB'`); err != nil {
		return fmt.Errorf("store_pg: bulk set maintenance_work_mem: %w", err)
	}

	if s.bulk.tableEmpty {
		// ============================================================
		// FAST PATH: destructive table swap (first repo, empty store).
		// ============================================================

		// Create UNLOGGED staging tables that will become the live tables.
		// INCLUDING ALL copies defaults, constraints, and indexes so the
		// swapped-in tables are fully formed.
		if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE nodes_bulk (LIKE nodes INCLUDING DEFAULTS)`); err != nil {
			return fmt.Errorf("store_pg: bulk create nodes_bulk: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE edges_bulk (LIKE edges INCLUDING DEFAULTS)`); err != nil {
			return fmt.Errorf("store_pg: bulk create edges_bulk: %w", err)
		}

		// COPY FROM nodes.
		if err := s.copyNodesBulk(ctx, tx); err != nil {
			return err
		}

		// COPY FROM edges.
		if err := s.copyEdgesBulk(ctx, tx); err != nil {
			return err
		}

		// Deduplicate edges: COPY FROM has no ON CONFLICT support,
		// so duplicate keys would violate the UNIQUE constraint below.
		// Self-join keeps the row with the smallest ctid per group.
		if _, err := tx.Exec(ctx, `
			DELETE FROM edges_bulk a
			USING edges_bulk b
			WHERE a.from_id = b.from_id
			  AND a.to_id = b.to_id
			  AND a.kind = b.kind
			  AND a.file_path = b.file_path
			  AND a.line = b.line
			  AND a.ctid > b.ctid
		`); err != nil {
			return fmt.Errorf("store_pg: deduplicate edges: %w", err)
		}

		// Build constraints and indexes on staging tables.
		// These must match the schema in schema.go exactly.
		indexDDL := []string{
			// nodes: primary key and secondary indexes
			`ALTER TABLE nodes_bulk ADD PRIMARY KEY (id)`,
			`CREATE INDEX ON nodes_bulk(name)`,
			`CREATE INDEX ON nodes_bulk(kind)`,
			`CREATE INDEX ON nodes_bulk(file_path)`,
			`CREATE INDEX ON nodes_bulk(repo_prefix) WHERE repo_prefix <> ''`,
			`CREATE UNIQUE INDEX ON nodes_bulk(qual_name) WHERE qual_name <> ''`,
			`CREATE INDEX ON nodes_bulk USING GIN (name gin_trgm_ops)`,
			// edges: unique constraint and secondary indexes
			`ALTER TABLE edges_bulk ADD UNIQUE (from_id, to_id, kind, file_path, line)`,
			`CREATE INDEX ON edges_bulk(from_id, kind)`,
			`CREATE INDEX ON edges_bulk(to_id)`,
			`CREATE INDEX ON edges_bulk(kind)`,
		}
		for _, ddl := range indexDDL {
			if _, err := tx.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("store_pg: bulk create index: %w (DDL: %s)", err, ddl)
			}
		}

		// Atomic swap: rename live → old, rename staging → live, drop old.
		if _, err := tx.Exec(ctx, `ALTER TABLE nodes RENAME TO nodes_old`); err != nil {
			return fmt.Errorf("store_pg: bulk rename nodes old: %w", err)
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE nodes_bulk RENAME TO nodes`); err != nil {
			return fmt.Errorf("store_pg: bulk rename nodes new: %w", err)
		}
		if _, err := tx.Exec(ctx, `DROP TABLE nodes_old CASCADE`); err != nil {
			return fmt.Errorf("store_pg: bulk drop nodes_old: %w", err)
		}

		if _, err := tx.Exec(ctx, `ALTER TABLE edges RENAME TO edges_old`); err != nil {
			return fmt.Errorf("store_pg: bulk rename edges old: %w", err)
		}
		if _, err := tx.Exec(ctx, `ALTER TABLE edges_bulk RENAME TO edges`); err != nil {
			return fmt.Errorf("store_pg: bulk rename edges new: %w", err)
		}
		// The BIGSERIAL id column in edges_bulk (copied via INCLUDING
		// DEFAULTS) references edges_id_seq — the same sequence as the
		// original edges table. Detach the sequence from the old table
		// before dropping it, then reattach to the new table.
		if _, err := tx.Exec(ctx, `ALTER SEQUENCE IF EXISTS edges_id_seq OWNED BY NONE`); err != nil {
			return fmt.Errorf("store_pg: detach edges sequence: %w", err)
		}
		if _, err := tx.Exec(ctx, `DROP TABLE edges_old`); err != nil {
			return fmt.Errorf("store_pg: bulk drop edges_old: %w", err)
		}
		if _, err := tx.Exec(ctx, `ALTER SEQUENCE IF EXISTS edges_id_seq OWNED BY edges.id`); err != nil {
			return fmt.Errorf("store_pg: reattach edges sequence: %w", err)
		}
	} else {
		// ================================================================
		// SAFE PATH: non-destructive merge (repos 2+, warm restart).
		// Staging tables carry no indexes — they are pure COPY FROM sinks.
		// ================================================================

		// Create bare UNLOGGED staging tables (no indexes, no constraints
		// beyond column types — just fast COPY FROM targets).
		if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE nodes_bulk (LIKE nodes INCLUDING DEFAULTS)`); err != nil {
			return fmt.Errorf("store_pg: bulk create nodes_bulk: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE UNLOGGED TABLE edges_bulk (LIKE edges INCLUDING DEFAULTS)`); err != nil {
			return fmt.Errorf("store_pg: bulk create edges_bulk: %w", err)
		}

		// COPY FROM into staging (same fast protocol as the swap path).
		if err := s.copyNodesBulk(ctx, tx); err != nil {
			return err
		}
		if err := s.copyEdgesBulk(ctx, tx); err != nil {
			return err
		}

		// Merge nodes from staging into the live table.
		// ON CONFLICT (id) DO UPDATE: same full-column upsert as AddBatch.
		if _, err := tx.Exec(ctx, `
			INSERT INTO nodes (`+nodeInsertCols+`)
			SELECT `+nodeInsertCols+` FROM nodes_bulk
			`+nodeInsertConflict); err != nil {
			return fmt.Errorf("store_pg: merge nodes from staging: %w", err)
		}

		// Merge edges from staging into the live table.
		// ON CONFLICT DO NOTHING: same insert-or-ignore as AddBatch.
		if _, err := tx.Exec(ctx, `
			INSERT INTO edges (`+edgeInsertCols+`)
			SELECT `+edgeInsertCols+` FROM edges_bulk
			ON CONFLICT (from_id, to_id, kind, file_path, line) DO NOTHING`); err != nil {
			return fmt.Errorf("store_pg: merge edges from staging: %w", err)
		}

		// Drop staging tables (no longer needed).
		if _, err := tx.Exec(ctx, `DROP TABLE nodes_bulk`); err != nil {
			return fmt.Errorf("store_pg: drop nodes_bulk: %w", err)
		}
		if _, err := tx.Exec(ctx, `DROP TABLE edges_bulk`); err != nil {
			return fmt.Errorf("store_pg: drop edges_bulk: %w", err)
		}
	}

	// Populate content_fts and other sidecars — they are not swapped but
	// will be populated by their normal AppendContent / BulkSet* paths after
	// this bulk phase completes.

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store_pg: bulk commit: %w", err)
	}

	return nil
}

// copyNodesBulk streams buffered node rows into the staging table via COPY FROM.
func (s *Store) copyNodesBulk(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"nodes_bulk"},
		parseCopyCols(nodeInsertCols),
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
	return nil
}

// copyEdgesBulk streams buffered edge rows into the staging table via COPY FROM.
func (s *Store) copyEdgesBulk(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.CopyFrom(ctx,
		pgx.Identifier{"edges_bulk"},
		parseCopyCols(edgeInsertCols),
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
	return nil
}

// AddBatchBulk is the bulk-aware variant of AddBatch. When a bulk-load
// session is active rows are buffered in memory; otherwise it delegates
// to the normal transactional INSERT path.
func (s *Store) AddBatchBulk(nodes []*graph.Node, edges []*graph.Edge) {
	s.AddBatch(nodes, edges)
}

// bufferBatchLocked appends nodes and edges to the bulk buffer.
// The caller must hold s.writeMu.
func (s *Store) bufferBatchLocked(nodes []*graph.Node, edges []*graph.Edge) {
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
