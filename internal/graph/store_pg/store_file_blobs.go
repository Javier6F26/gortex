package store_pg

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zzet/gortex/internal/graph"
)

// Content-addressed file-blob storage (code-source-blobs). Blobs let a
// diskless follower serve byte-exact source: the file's content_hash (from
// the files table) joins to file_blobs.body.

var (
	_ graph.FileBlobWriter        = (*Store)(nil)
	_ graph.FileBlobReader        = (*Store)(nil)
	_ graph.IndexedFileBlobLister = (*Store)(nil)
)

// IndexedFileBlobs returns every indexed file that has a stored blob (joined
// via files.content_hash), so a diskless follower can build its text /
// structural search over the exact set of files it can serve byte-exact.
func (s *Store) IndexedFileBlobs() ([]graph.IndexedFileRef, error) {
	var out []graph.IndexedFileRef
	var qerr error
	s.withReadRetry("IndexedFileBlobs", func() error {
		out = nil
		rows, err := s.pool.Query(s.ctx,
			`SELECT f.repo_prefix, f.file_path, f.content_hash
			 FROM files f JOIN file_blobs fb ON fb.content_hash = f.content_hash`)
		if err != nil {
			qerr = err
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r graph.IndexedFileRef
			if err := rows.Scan(&r.RepoPrefix, &r.FilePath, &r.ContentHash); err != nil {
				qerr = err
				return err
			}
			out = append(out, r)
		}
		qerr = rows.Err()
		return qerr
	})
	return out, qerr
}

// PutFileBlobs inserts blobs, deduplicating on content_hash (a hash already
// present is left untouched — content addressing means the bytes are
// identical). Idempotent across repos and re-indexes.
func (s *Store) PutFileBlobs(blobs []graph.FileBlob) error {
	if s.refuseWrite("PutFileBlobs") {
		return ErrReadOnlyStore
	}
	if len(blobs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.pool.Begin(s.ctx)
	if err != nil {
		return err
	}
	for _, b := range blobs {
		if b.ContentHash == "" {
			continue
		}
		if _, err := tx.Exec(s.ctx,
			`INSERT INTO file_blobs (content_hash, body, size) VALUES ($1, $2, $3)
			 ON CONFLICT (content_hash) DO NOTHING`,
			b.ContentHash, b.Body, b.Size); err != nil {
			_ = tx.Rollback(s.ctx)
			return err
		}
	}
	return tx.Commit(s.ctx)
}

// GCFileBlobs deletes blobs whose content_hash is no longer referenced by any
// files row. Run after a flush so re-indexed / vanished files don't leave
// orphan bytes. Returns the number removed.
func (s *Store) GCFileBlobs() (int, error) {
	if s.refuseWrite("GCFileBlobs") {
		return 0, ErrReadOnlyStore
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.pool.Exec(s.ctx,
		`DELETE FROM file_blobs b
		 WHERE NOT EXISTS (SELECT 1 FROM files f WHERE f.content_hash = b.content_hash)`)
	if err != nil {
		return 0, err
	}
	return int(res.RowsAffected()), nil
}

// GetFileBlobByPath returns the indexed bytes for (repo_prefix, file_path) by
// joining files.content_hash to file_blobs. Routes through the retry/degrade
// read path.
func (s *Store) GetFileBlobByPath(repoPrefix, filePath string) (graph.FileBlob, bool) {
	var blob graph.FileBlob
	var found bool
	s.withReadRetry("GetFileBlobByPath", func() error {
		blob = graph.FileBlob{}
		found = false
		var body []byte
		var size int
		var hash string
		err := s.pool.QueryRow(s.ctx,
			`SELECT fb.content_hash, fb.body, fb.size
			 FROM files f JOIN file_blobs fb ON fb.content_hash = f.content_hash
			 WHERE f.repo_prefix = $1 AND f.file_path = $2`,
			repoPrefix, filePath).Scan(&hash, &body, &size)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		blob = graph.FileBlob{ContentHash: hash, Body: body, Size: size}
		found = true
		return nil
	})
	return blob, found
}

// GetFileBlobByHash returns the blob bytes for a content hash.
func (s *Store) GetFileBlobByHash(hash string) (graph.FileBlob, bool) {
	var blob graph.FileBlob
	var found bool
	s.withReadRetry("GetFileBlobByHash", func() error {
		blob = graph.FileBlob{}
		found = false
		var body []byte
		var size int
		err := s.pool.QueryRow(s.ctx,
			`SELECT body, size FROM file_blobs WHERE content_hash = $1`, hash).Scan(&body, &size)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		blob = graph.FileBlob{ContentHash: hash, Body: body, Size: size}
		found = true
		return nil
	})
	return blob, found
}
