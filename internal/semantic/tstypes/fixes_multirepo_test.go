package tstypes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// indexRepoInto extracts every file under one repo root and adds the
// nodes/edges to g under the given repo prefix — the same prefixing the
// MultiIndexer applies (node ID / FilePath / edge endpoints gain a
// `prefix/` and RepoPrefix is stamped). Returns the on-disk root.
func indexRepoInto(t *testing.T, g *graph.Graph, prefix string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		lang, ok := reg.DetectLanguage(rel)
		if !ok {
			t.Fatalf("no language for %s", rel)
		}
		ext, ok := reg.GetByLanguage(lang)
		if !ok {
			t.Fatalf("no extractor for %s", lang)
		}
		res, err := ext.Extract(rel, []byte(content))
		if err != nil {
			t.Fatalf("extract %s: %v", rel, err)
		}
		if res.Tree != nil {
			res.Tree.Close()
		}
		prefixNodesEdges(prefix, res.Nodes, res.Edges)
		g.AddBatch(res.Nodes, res.Edges)
	}
	return dir
}

// prefixNodesEdges mirrors the indexer's repo-prefixing for test graphs.
func prefixNodesEdges(prefix string, nodes []*graph.Node, edges []*graph.Edge) {
	if prefix == "" {
		return
	}
	p := prefix + "/"
	for _, n := range nodes {
		n.ID = p + n.ID
		n.FilePath = p + n.FilePath
		n.RepoPrefix = prefix
	}
	for _, e := range edges {
		e.From = p + e.From
		if !strings.HasPrefix(e.To, "unresolved::") {
			e.To = p + e.To
		}
		e.FilePath = p + e.FilePath
	}
}

// In multi-repo mode two repos can share a relative path. languageFiles
// must scope file selection to the repo actually being enriched — never
// read repo A's bytes for repo B's node just because the relative path
// happens to exist under both roots.
func TestEnrich_MultiRepoPathCollisionDoesNotContaminate(t *testing.T) {
	g := graph.New()

	// Both repos define pkg/Svc.java + pkg/App.java at the SAME relative
	// paths, but with different method names. Repo A's App calls a.run();
	// repo B's App calls b.go(). If file selection leaked across repos,
	// enriching one root would parse the other repo's bytes for the
	// colliding path.
	repoA := map[string]string{
		"pkg/Svc.java": `package pkg;
public class Svc {
    public void run() {}
}
`,
		"pkg/App.java": `package pkg;
public class App {
    public void main() {
        Svc s = new Svc();
        s.run();
    }
}
`,
	}
	repoB := map[string]string{
		"pkg/Svc.java": `package pkg;
public class Svc {
    public void go() {}
}
`,
		"pkg/App.java": `package pkg;
public class App {
    public void main() {
        Svc s = new Svc();
        s.go();
    }
}
`,
	}
	rootA := indexRepoInto(t, g, "repoA", repoA)
	rootB := indexRepoInto(t, g, "repoB", repoB)

	p := NewProvider(JavaSpec(), zap.NewNop())
	if _, err := p.EnrichRepo(g, "repoA", rootA); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnrichRepo(g, "repoB", rootB); err != nil {
		t.Fatal(err)
	}

	// Repo A's main must call repoA Svc.run, and nothing in repo A may
	// point at a repoB target.
	mainA := "repoA/pkg/App.java::App.main"
	if callEdgeTo(g, mainA, "repoA/pkg/Svc.java::Svc.run") == nil {
		t.Fatalf("repo A call run() not resolved within repo A; edges: %v", g.GetOutEdges(mainA))
	}
	for _, e := range g.GetOutEdges(mainA) {
		if strings.HasPrefix(e.To, "repoB/") {
			t.Fatalf("repo A edge leaked into repo B target: %s -> %s", e.From, e.To)
		}
	}

	// Repo B's main must call repoB Svc.go, and nothing in repo B may
	// point at a repoA target.
	mainB := "repoB/pkg/App.java::App.main"
	if callEdgeTo(g, mainB, "repoB/pkg/Svc.java::Svc.go") == nil {
		t.Fatalf("repo B call go() not resolved within repo B; edges: %v", g.GetOutEdges(mainB))
	}
	for _, e := range g.GetOutEdges(mainB) {
		if strings.HasPrefix(e.To, "repoA/") {
			t.Fatalf("repo B edge leaked into repo A target: %s -> %s", e.From, e.To)
		}
	}

	_ = rootB
}
