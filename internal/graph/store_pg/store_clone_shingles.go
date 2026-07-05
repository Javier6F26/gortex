package store_pg

import (
	"encoding/binary"

	"github.com/zzet/gortex/internal/graph"
)

// Clone shingle persistence for clone detection CMS rebuild.

var (
	_ graph.CloneShingleWriter = (*Store)(nil)
	_ graph.CloneShingleReader = (*Store)(nil)
)

func (s *Store) BulkSetCloneShingles(repoPrefix string, rows map[string][]uint64) error {
	if len(rows) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	for nodeID, shingles := range rows {
		blob := encodeShingles(shingles)
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO clone_shingles (node_id, repo_prefix, shingles) VALUES ($1, $2, $3)
			 ON CONFLICT (node_id) DO UPDATE SET shingles = EXCLUDED.shingles`,
			nodeID, repoPrefix, blob); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteCloneShingles(nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx,
		`DELETE FROM clone_shingles WHERE node_id = ANY($1)`, nodeIDs)
	return err
}

func (s *Store) LoadCloneShingles(repoPrefix string) (map[string][]uint64, error) {
	rows, err := s.pool.Query(s.ctx,
		`SELECT node_id, shingles FROM clone_shingles WHERE repo_prefix = $1`, repoPrefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]uint64)
	for rows.Next() {
		var nodeID string
		var blob []byte
		if err := rows.Scan(&nodeID, &blob); err != nil {
			return out, err
		}
		out[nodeID] = decodeShingles(blob)
	}
	return out, rows.Err()
}

func encodeShingles(shingles []uint64) []byte {
	b := make([]byte, len(shingles)*8)
	for i, s := range shingles {
		binary.LittleEndian.PutUint64(b[i*8:], s)
	}
	return b
}

func decodeShingles(b []byte) []uint64 {
	if len(b) == 0 || len(b)%8 != 0 {
		return nil
	}
	out := make([]uint64, len(b)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(b[i*8:])
	}
	return out
}
