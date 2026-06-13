package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
	"github.com/zzet/gortex/internal/semantic"
	"github.com/zzet/gortex/internal/semantic/tstypes"
)

// A single-file save must re-run incremental semantic enrichment so the
// file's edges are re-confirmed by the in-process type resolvers, rather
// than staying at their pre-enrichment tier until the next full reindex.
// This exercises the indexFile -> Manager.EnrichFile wiring end to end.
func TestIndexFile_RunsIncrementalSemanticEnrichment(t *testing.T) {
	dir := t.TempDir()

	svc := filepath.Join(dir, "a", "Svc.java")
	app := filepath.Join(dir, "b", "App.java")
	require.NoError(t, os.MkdirAll(filepath.Dir(svc), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(app), 0o755))
	require.NoError(t, os.WriteFile(svc, []byte(`package a;

public class Svc {
    public void run() {}
}
`), 0o644))
	require.NoError(t, os.WriteFile(app, []byte(`package b;

import a.Svc;

public class App {
    public void handle(Svc s) {
    }
}
`), 0o644))

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)
	cfg := config.Default().Index
	cfg.Workers = 2
	idx := New(g, reg, cfg, zap.NewNop())

	// Semantic manager with the in-process type resolvers and watch-mode
	// enrichment enabled.
	mgr := semantic.NewManager(semantic.Config{Enabled: true, EnrichOnWatch: true}, zap.NewNop())
	for _, p := range tstypes.DefaultProviders(zap.NewNop()) {
		mgr.RegisterProvider(p)
	}
	idx.SetSemanticManager(mgr)

	if _, err := idx.Index(dir); err != nil {
		t.Fatalf("index: %v", err)
	}

	caller := "b/App.java::App.handle"
	target := "a/Svc.java::Svc.run"

	// At this point handle() has no body call. Now edit App.java to add a
	// receiver-qualified call and re-index just that file.
	require.NoError(t, os.WriteFile(app, []byte(`package b;

import a.Svc;

public class App {
    public void handle(Svc s) {
        s.run();
    }
}
`), 0o644))

	require.NoError(t, idx.IndexFile(app))

	// The incremental semantic pass must have resolved + stamped the call.
	var e *graph.Edge
	for _, oe := range g.GetOutEdges(caller) {
		if oe.Kind == graph.EdgeCalls && oe.To == target {
			e = oe
			break
		}
	}
	require.NotNilf(t, e, "incremental semantic enrichment did not resolve the call; edges: %v", g.GetOutEdges(caller))
	require.Equal(t, "ast_resolved", e.Origin, "edge not stamped by the in-process type resolver")
	require.NotNil(t, e.Meta)
	require.Equal(t, "java-types", e.Meta["semantic_source"])
}
