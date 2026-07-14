package trigram

import (
	"os"
	"reflect"
	"regexp"
	"testing"
)

// mapSource is an in-memory FileSource — the shape a blob-backed follower
// supplies (bytes keyed by graph path, not read from disk).
type mapSource map[string][]byte

func (m mapSource) ReadFile(rel string) ([]byte, error) {
	b, ok := m[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

// A blob-backed FileSource produces byte-identical literal and regex search
// results to the disk-backed DirSource over the same content (code-source-blobs
// D7: search_text works diskless with parity to the on-disk run).
func TestFileSource_DiskVsBlobParity(t *testing.T) {
	files := map[string]string{
		"a/foo.go": "package a\n\nfunc Alpha() int { return 1 }\n// TODO tidy\n",
		"a/bar.go": "package a\n\nfunc Beta() string { return \"beta\" }\n",
		"b/baz.go": "package b\n\nfunc Alpha() {}\n// TODO refactor Alpha\n",
	}
	rels := []string{"a/foo.go", "a/bar.go", "b/baz.go"}

	// Disk-backed searcher.
	root := t.TempDir()
	src := mapSource{}
	for rel, content := range files {
		mk(t, root, rel, content)
		src[rel] = []byte(content)
	}
	disk := Build(root, rels)
	blob := BuildFromSource(src, rels)

	if disk.DocCount() != blob.DocCount() {
		t.Fatalf("doc count disk=%d blob=%d", disk.DocCount(), blob.DocCount())
	}

	for _, q := range []string{"Alpha", "TODO", "func", "return", "nonexistent"} {
		d := disk.Grep(q, 0)
		b := blob.Grep(q, 0)
		if !reflect.DeepEqual(d, b) {
			t.Errorf("literal Grep(%q) parity mismatch:\n disk=%+v\n blob=%+v", q, d, b)
		}
	}

	for _, pat := range []string{`TODO\s+\w+`, `func \w+\(`, `Alpha`} {
		re := regexp.MustCompile(pat)
		lits := []string{}
		if idx := regexp.MustCompile(`[A-Za-z]{3,}`).FindString(pat); idx != "" {
			lits = append(lits, idx)
		}
		d := disk.GrepRegexp(re, lits, "", 0)
		b := blob.GrepRegexp(re, lits, "", 0)
		if !reflect.DeepEqual(d, b) {
			t.Errorf("regex GrepRegexp(%q) parity mismatch:\n disk=%+v\n blob=%+v", pat, d, b)
		}
	}
}

// A file the source cannot supply is left unindexed (never matches) but keeps
// the other docIDs aligned — parity with the disk path's unreadable-file rule.
func TestFileSource_MissingFileUnindexed(t *testing.T) {
	src := mapSource{"present.go": []byte("func Here() {}\n")}
	s := BuildFromSource(src, []string{"present.go", "absent.go"})
	if got := s.Grep("Here", 0); len(got) != 1 || got[0].Path != "present.go" {
		t.Fatalf("present file should match: %+v", got)
	}
	if got := s.Grep("anything", 0); len(got) != 0 {
		t.Fatalf("absent file must never match, got %+v", got)
	}
}

// DirSource joins root + rel exactly like the legacy os.ReadFile path.
func TestDirSource_ReadsRootRelative(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "sub/x.go", "hello")
	b, err := (DirSource{Root: root}).ReadFile("sub/x.go")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
	if _, err := (DirSource{Root: root}).ReadFile("nope.go"); err == nil {
		t.Fatal("expected error for missing file")
	}
}
