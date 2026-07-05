package store_pg

// DB stats introspection for monitoring and diagnostics.

// DBSize returns the estimated database size in bytes, including indexes.
func (s *Store) DBSize() (int64, error) {
	var size int64
	err := s.pool.QueryRow(s.ctx,
		`SELECT COALESCE(SUM(pg_total_relation_size(relid)), 0) FROM pg_stat_user_tables`).Scan(&size)
	return size, err
}

// DBTableSizes returns per-table size info (table name, total bytes, row count).
type DBTableSize struct {
	TableName  string
	TotalBytes int64
	RowCount   int64
}

func (s *Store) DBTableSizes() ([]DBTableSize, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT relname AS table_name,
		        pg_total_relation_size(relid) AS total_bytes,
		        COALESCE(n_live_tup, 0) AS row_count
		 FROM pg_stat_user_tables
		 ORDER BY total_bytes DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DBTableSize
	for rows.Next() {
		var t DBTableSize
		if err := rows.Scan(&t.TableName, &t.TotalBytes, &t.RowCount); err != nil {
			return out, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PoolStats returns the current pgxpool statistics.
type PoolStats struct {
	MaxConns    int32
	AcquiredConns int32
	IdleConns   int32
	TotalConns  int32
}

func (s *Store) PoolStats() PoolStats {
	st := s.pool.Stat()
	return PoolStats{
		MaxConns:       st.MaxConns(),
		AcquiredConns: st.AcquiredConns(),
		IdleConns:     st.IdleConns(),
		TotalConns:    st.TotalConns(),
	}
}
