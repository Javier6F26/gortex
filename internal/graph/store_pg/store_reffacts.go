package store_pg

import (
	"github.com/zzet/gortex/internal/graph"
)

// Ref facts persistence for auditable, durable resolved-reference records.

var (
	_ graph.RefFactsWriter = (*Store)(nil)
	_ graph.RefFactsReader = (*Store)(nil)
)

func (s *Store) BulkSetRefFacts(repoPrefix string, facts []graph.RefFact) error {
	if s.refuseWrite("BulkSetRefFacts") { return ErrReadOnlyStore }
	if len(facts) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	for _, f := range facts {
		// Delete existing then insert (full replace for the key)
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO ref_facts (repo_prefix, from_id, to_id, kind, ref_name, line, origin, tier, candidates, file_path, lang)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 ON CONFLICT (repo_prefix, from_id, to_id, kind, line)
			 DO UPDATE SET to_id = EXCLUDED.to_id, origin = EXCLUDED.origin, tier = EXCLUDED.tier,
			   candidates = EXCLUDED.candidates, file_path = EXCLUDED.file_path, lang = EXCLUDED.lang`,
			f.RepoPrefix, f.FromID, f.ToID, f.Kind, f.RefName, f.Line,
			f.Origin, f.Tier, encodeCandidates(f.Candidates), f.FilePath, f.Lang); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

func (s *Store) DeleteRefFactsByFiles(repoPrefix string, files []string) error {
	if s.refuseWrite("DeleteRefFactsByFiles") { return ErrReadOnlyStore }
	if len(files) == 0 {
		return nil
	}
	_, err := s.pool.Exec(s.ctx,
		`DELETE FROM ref_facts WHERE repo_prefix = $1 AND file_path = ANY($2)`, repoPrefix, files)
	return err
}

func (s *Store) LoadRefFactsByFiles(repoPrefix string, files []string) ([]graph.RefFact, error) {
	var args []any
	q := `SELECT repo_prefix, from_id, to_id, kind, ref_name, line, origin, tier, candidates, file_path, lang FROM ref_facts WHERE repo_prefix = $1`
	args = append(args, repoPrefix)
	if len(files) > 0 {
		q += ` AND file_path = ANY($2)`
		args = append(args, files)
	}

	rows, err := s.pool.Query(s.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []graph.RefFact
	for rows.Next() {
		var f graph.RefFact
		var candidatesStr string
		if err := rows.Scan(&f.RepoPrefix, &f.FromID, &f.ToID, &f.Kind, &f.RefName, &f.Line,
			&f.Origin, &f.Tier, &candidatesStr, &f.FilePath, &f.Lang); err != nil {
			return out, err
		}
		f.Candidates = decodeCandidates(candidatesStr)
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) LoadRefFactsByTargets(repoPrefix string, targetIDs []string) (map[string][]graph.RefFact, error) {
	if len(targetIDs) == 0 {
		return map[string][]graph.RefFact{}, nil
	}

	q := `SELECT repo_prefix, from_id, to_id, kind, ref_name, line, origin, tier, candidates, file_path, lang
	      FROM ref_facts WHERE repo_prefix = $1 AND to_id = ANY($2)`

	rows, err := s.pool.Query(s.ctx, q, repoPrefix, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]graph.RefFact)
	for rows.Next() {
		var f graph.RefFact
		var candidatesStr string
		if err := rows.Scan(&f.RepoPrefix, &f.FromID, &f.ToID, &f.Kind, &f.RefName, &f.Line,
			&f.Origin, &f.Tier, &candidatesStr, &f.FilePath, &f.Lang); err != nil {
			return out, err
		}
		f.Candidates = decodeCandidates(candidatesStr)
		out[f.FilePath] = append(out[f.FilePath], f)
	}
	return out, rows.Err()
}

func encodeCandidates(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	out := make([]byte, 0, len(candidates)*40)
	for i, c := range candidates {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, c...)
	}
	return string(out)
}

func decodeCandidates(s string) []string {
	if s == "" {
		return nil
	}
	// Simple split by newline.
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
