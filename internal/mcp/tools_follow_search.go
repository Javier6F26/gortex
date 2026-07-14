package mcp

import (
	"os"
	"regexp/syntax"

	"github.com/zzet/gortex/internal/astquery"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/search/trigram"
)

// extractRegexLiterals returns the mandatory literal substrings (>= 3 bytes)
// a regexp must contain on every match, used to trigram-pre-filter the
// candidate file set. Mirrors the indexer's extractor (internal/indexer/
// grep.go) so a follower's regex search narrows files the same way. A pattern
// that does not parse yields no literals — safe: the scan falls back to every
// file and the compiled regexp still verifies each line.
func extractRegexLiterals(pattern string) []string {
	reSyn, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(re *syntax.Regexp)
	walk = func(re *syntax.Regexp) {
		switch re.Op {
		case syntax.OpLiteral:
			if s := string(re.Rune); len(s) >= 3 {
				out = append(out, s)
			}
		case syntax.OpConcat, syntax.OpCapture, syntax.OpPlus:
			for _, sub := range re.Sub {
				walk(sub)
			}
		}
	}
	walk(reSyn)
	return out
}

// blobFileSource is the blob-backed trigram.FileSource used by a diskless
// follower: it maps a forward-slash graph path to the content hash captured
// at build time and reads the byte-exact blob from the store. A path with no
// captured hash (or a missing blob) reads as os.ErrNotExist, so the file is
// left unindexed / unscanned exactly like an unreadable disk file.
type blobFileSource struct {
	reader graph.FileBlobReader
	hashes map[string]string // graph path -> content_hash
}

// ReadFile returns the indexed bytes for rel via its content hash.
func (b blobFileSource) ReadFile(rel string) ([]byte, error) {
	hash, ok := b.hashes[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	blob, found := b.reader.GetFileBlobByHash(hash)
	if !found {
		return nil, os.ErrNotExist
	}
	return blob.Body, nil
}

// followBlobHashes enumerates the follower's servable file set once, returning
// a graph-path → content-hash map and the store's blob reader. ok is false
// when the backend can't serve blobs (pre-blob schema, non-PG store) or has no
// indexed files. Cached for the process life alongside the trigram searcher —
// a v0 snapshot; freshness lags the writer (cache invalidation on publish is
// out of scope) and self-corrects on follower restart. Caller must NOT hold
// followSearcherMu.
func (s *Server) followBlobHashes() (map[string]string, graph.FileBlobReader, bool) {
	lister, ok := s.graph.(graph.IndexedFileBlobLister)
	if !ok {
		return nil, nil, false
	}
	reader, ok := s.graph.(graph.FileBlobReader)
	if !ok {
		return nil, nil, false
	}
	refs, err := lister.IndexedFileBlobs()
	if err != nil || len(refs) == 0 {
		return nil, nil, false
	}
	hashes := make(map[string]string, len(refs))
	for _, r := range refs {
		if r.FilePath == "" || r.ContentHash == "" {
			continue
		}
		if _, seen := hashes[r.FilePath]; !seen {
			hashes[r.FilePath] = r.ContentHash
		}
	}
	if len(hashes) == 0 {
		return nil, nil, false
	}
	return hashes, reader, true
}

// followTrigramSearcher lazily builds and caches the follower's blob-backed
// trigram searcher. It enumerates every indexed file that has a blob and
// builds a trigram index reading bytes from file_blobs — so search_text works
// on a daemon with no working tree. Returns nil when no blobs are servable.
//
// The searcher is a v0 snapshot: built once, cached for the process life.
func (s *Server) followTrigramSearcher() *trigram.Searcher {
	s.followSearcherMu.Lock()
	defer s.followSearcherMu.Unlock()
	if s.followSearcherDone {
		return s.followSearcher
	}
	s.followSearcherDone = true // build at most once, even on failure

	hashes, reader, ok := s.followBlobHashes()
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(hashes))
	for p := range hashes {
		paths = append(paths, p)
	}
	s.followSearcher = trigram.BuildFromSource(blobFileSource{reader: reader, hashes: hashes}, paths)
	return s.followSearcher
}

// followASTSource returns an astquery.Source that reads a target's bytes from
// the store's file_blobs by graph path — the FileSource seam that lets
// search_ast parse diskless on a follower. Returns nil when no blobs are
// servable, so the caller can fall through to a typed error.
func (s *Server) followASTSource() func(astquery.Target) ([]byte, error) {
	hashes, reader, ok := s.followBlobHashes()
	if !ok {
		return nil
	}
	return func(t astquery.Target) ([]byte, error) {
		hash, ok := hashes[t.GraphPath]
		if !ok {
			return nil, os.ErrNotExist
		}
		blob, found := reader.GetFileBlobByHash(hash)
		if !found {
			return nil, os.ErrNotExist
		}
		return blob.Body, nil
	}
}
