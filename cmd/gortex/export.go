package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/exporter"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/indexer"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

var (
	exportFormat        string
	exportOut           string
	exportRepo          string
	exportKinds         []string
	exportLanguages     []string
	exportDropSynthetic bool
)

var exportCmd = &cobra.Command{
	Use:   "export [path]",
	Short: "Export the graph to Cypher (Neo4j/Memgraph) or GraphML (yEd/Gephi/Cytoscape)",
	Long: `Export the in-memory graph to a portable file for visualization or
external query. The current working directory is indexed first; pass [path] to
index a specific repo. The graph is not persisted between runs — each export
re-indexes.

Loading a Cypher export into Neo4j:
  cypher-shell -u neo4j -p <password> -f graph.cypher

Loading a Cypher export into Memgraph (Docker):
  docker run -it -p 7687:7687 -p 3000:3000 memgraph/memgraph-platform
  # then in Memgraph Lab → Query Modules → Run query: load the file

Loading a GraphML export into Gephi: File → Open → graph.graphml
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "cypher", "output format: cypher | graphml")
	exportCmd.Flags().StringVar(&exportOut, "out", "", "output file (default: stdout)")
	exportCmd.Flags().StringVar(&exportRepo, "repo", "", "filter to one repo prefix (default: all)")
	exportCmd.Flags().StringSliceVar(&exportKinds, "kinds", nil,
		"comma-separated node kinds to include (function,method,field,type,interface,...). Default: all.")
	exportCmd.Flags().StringSliceVar(&exportLanguages, "languages", nil,
		"comma-separated languages to include. Default: all.")
	exportCmd.Flags().BoolVar(&exportDropSynthetic, "no-synthetic", false,
		"drop synthetic stub nodes for unresolved/external/annotation endpoints (default: keep them so call topology stays intact)")
	rootCmd.AddCommand(exportCmd)
}

func runExport(_ *cobra.Command, args []string) error {
	logger := newLogger()
	defer func() { _ = logger.Sync() }()

	path := "."
	if len(args) == 1 {
		path = args[0]
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	g := graph.New()
	reg := parser.NewRegistry()
	languages.RegisterAll(reg)

	idx := indexer.New(g, reg, cfg.Index, logger)

	indexStart := time.Now()
	if _, err := idx.IndexCtx(context.Background(), path); err != nil {
		return fmt.Errorf("index %q: %w", path, err)
	}
	idx.ResolveAll()
	indexDuration := time.Since(indexStart)

	stats := g.Stats()
	fmt.Fprintf(os.Stderr, "[gortex export] indexed %s: %d nodes, %d edges in %dms\n",
		path, stats.TotalNodes, stats.TotalEdges, indexDuration.Milliseconds())

	opts := exporter.Options{
		Repo:          exportRepo,
		Languages:     exportLanguages,
		DropSynthetic: exportDropSynthetic,
	}
	for _, k := range exportKinds {
		opts.Kinds = append(opts.Kinds, graph.NodeKind(strings.ToLower(strings.TrimSpace(k))))
	}

	out, closeFn, err := openOutput(exportOut)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer closeFn()

	exportStart := time.Now()
	var exportStats exporter.Stats
	switch strings.ToLower(exportFormat) {
	case "cypher":
		exportStats, err = exporter.WriteCypher(out, g, opts)
	case "graphml":
		exportStats, err = exporter.WriteGraphML(out, g, opts)
	default:
		return fmt.Errorf("unknown format %q (expected cypher | graphml)", exportFormat)
	}
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	exportDuration := time.Since(exportStart)

	dest := exportOut
	if dest == "" {
		dest = "stdout"
	}
	fmt.Fprintf(os.Stderr,
		"[gortex export] wrote %d nodes, %d edges (%d bytes) to %s in %dms\n",
		exportStats.NodesWritten, exportStats.EdgesWritten, exportStats.BytesWritten,
		dest, exportDuration.Milliseconds(),
	)
	return nil
}

// openOutput returns a writer for path, or os.Stdout when path is empty.
// The returned closer is always non-nil (it's a no-op for stdout).
func openOutput(path string) (*os.File, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}
