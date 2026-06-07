package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	exportFormat         string
	exportOut            string
	exportOutDir         string
	exportRepo           string
	exportKinds          []string
	exportLanguages      []string
	exportDropSynthetic  bool
	exportMermaidScope   string
	exportMermaidMinComm int
	exportMermaidMaxComm int
	exportOnCommit       bool
)

var exportCmd = &cobra.Command{
	Use:   "export [path]",
	Short: "Export the graph to Cypher (Neo4j/Memgraph) or GraphML (yEd/Gephi/Cytoscape)",
	Long: `Export the graph the daemon owns to a portable file for visualization or
external query. Runs export_graph against the daemon that tracks the repo
(requires a running daemon).

Loading a Cypher export into Neo4j:
  cypher-shell -u neo4j -p <password> -f graph.cypher

Loading a GraphML export into Gephi: File → Open → graph.graphml
`,
	Args: cobra.MaximumNArgs(1),
	RunE: runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "cypher", "output format: cypher | graphml | mermaid")
	exportCmd.Flags().StringVar(&exportOut, "out", "", "output file (default: stdout)")
	exportCmd.Flags().StringVar(&exportOutDir, "out-dir", "",
		"output directory (mermaid scope=all writes one file per scope here)")
	exportCmd.Flags().StringVar(&exportRepo, "repo", "", "filter to one repo prefix (default: all)")
	exportCmd.Flags().StringSliceVar(&exportKinds, "kinds", nil,
		"comma-separated node kinds to include (function,method,field,type,interface,...). Default: all.")
	exportCmd.Flags().StringSliceVar(&exportLanguages, "languages", nil,
		"comma-separated languages to include. Default: all.")
	exportCmd.Flags().BoolVar(&exportDropSynthetic, "no-synthetic", false,
		"drop synthetic stub nodes for unresolved/external/annotation endpoints (default: keep them so call topology stays intact)")
	exportCmd.Flags().StringVar(&exportMermaidScope, "scope", "architecture",
		"(mermaid) diagram scope: architecture | communities | processes | all")
	exportCmd.Flags().IntVar(&exportMermaidMinComm, "min-community", 3,
		"(mermaid) minimum community size to include")
	exportCmd.Flags().IntVar(&exportMermaidMaxComm, "max-communities", 20,
		"(mermaid) maximum communities to include")
	exportCmd.Flags().BoolVar(&exportOnCommit, "on-commit", false,
		"informational marker: this run was triggered by a post-commit hook")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	repoPath := "."
	if len(args) == 1 {
		repoPath = args[0]
	}
	format := strings.ToLower(exportFormat)

	toolArgs := map[string]any{
		"format":          format,
		"repo":            exportRepo,
		"kinds":           strings.Join(exportKinds, ","),
		"languages":       strings.Join(exportLanguages, ","),
		"no_synthetic":    exportDropSynthetic,
		"scope":           exportMermaidScope,
		"min_community":   exportMermaidMinComm,
		"max_communities": exportMermaidMaxComm,
	}

	// The daemon writes any output files, so paths must be absolute (its
	// cwd is not the user's).
	switch {
	case format == "mermaid" && exportOutDir != "":
		abs, err := filepath.Abs(exportOutDir)
		if err != nil {
			return fmt.Errorf("resolve out-dir: %w", err)
		}
		toolArgs["output_dir"] = abs
		if _, err := requireDaemonTool(repoPath, "export_graph", toolArgs); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex export] daemon wrote mermaid files under %s\n", abs)
		return nil

	case exportOut != "":
		abs, err := filepath.Abs(exportOut)
		if err != nil {
			return fmt.Errorf("resolve output path: %w", err)
		}
		toolArgs["output_path"] = abs
		if _, err := requireDaemonTool(repoPath, "export_graph", toolArgs); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "[gortex export] daemon wrote the export to %s\n", abs)
		printLoadInstructions(format, exportOut)
		return nil

	default:
		out, err := requireDaemonTool(repoPath, "export_graph", toolArgs)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(out)
		return err
	}
}

func printLoadInstructions(format, path string) {
	w := os.Stderr
	switch format {
	case "cypher":
		_, _ = fmt.Fprintf(w, "\n[gortex export] To load into Memgraph (recommended for ad-hoc exploration):\n")
		_, _ = fmt.Fprintf(w, "    docker run -p 7687:7687 -p 3000:3000 memgraph/memgraph-platform\n")
		_, _ = fmt.Fprintf(w, "    # then in Memgraph Lab (http://localhost:3000) → Datasets → Import\n")
		_, _ = fmt.Fprintf(w, "    # OR via mgconsole:  cat %s | mgconsole\n", path)
		_, _ = fmt.Fprintf(w, "    # First, create an id index for fast edge MATCHes:\n")
		_, _ = fmt.Fprintf(w, "    #   CREATE INDEX ON :GortexNode(id);\n")
		_, _ = fmt.Fprintf(w, "\n[gortex export] To load into Neo4j:\n")
		_, _ = fmt.Fprintf(w, "    cypher-shell -u neo4j -p <pw> -f %s\n", path)
		_, _ = fmt.Fprintf(w, "    # First, create the index:\n")
		_, _ = fmt.Fprintf(w, "    #   CREATE INDEX FOR (n:GortexNode) ON (n.id);\n")
		_, _ = fmt.Fprintf(w, "\n[gortex export] To wipe a previous export before re-loading:\n")
		_, _ = fmt.Fprintf(w, "    MATCH (n:GortexNode) DETACH DELETE n;\n")
	case "graphml":
		_, _ = fmt.Fprintf(w, "\n[gortex export] Open %s in:\n", path)
		_, _ = fmt.Fprintf(w, "    Gephi:     File → Open\n")
		_, _ = fmt.Fprintf(w, "    yEd:       File → Open\n")
		_, _ = fmt.Fprintf(w, "    Cytoscape: File → Import → Network from File\n")
	}
}
