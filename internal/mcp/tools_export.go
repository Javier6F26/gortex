package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/zzet/gortex/internal/exporter"
	"github.com/zzet/gortex/internal/graph"
)

func (s *Server) registerExportTools() {
	s.addTool(
		mcp.NewTool("export_graph",
			mcp.WithDescription("Export the graph to Cypher (Neo4j/Memgraph), GraphML (Gephi/yEd/Cytoscape), or Mermaid. With output_path / output_dir the daemon writes the export to disk; otherwise it is returned inline."),
			mcp.WithString("format", mcp.Description("cypher (default) | graphml | mermaid")),
			mcp.WithString("output_path", mcp.Description("Absolute path to write the export to; omit to return inline")),
			mcp.WithString("output_dir", mcp.Description("Absolute directory for mermaid scope=all (one file per scope)")),
			mcp.WithString("repo", mcp.Description("Filter to one repo prefix (default: all)")),
			mcp.WithString("kinds", mcp.Description("Comma-separated node kinds to include (default: all)")),
			mcp.WithString("languages", mcp.Description("Comma-separated languages to include (default: all)")),
			mcp.WithBoolean("no_synthetic", mcp.Description("Drop synthetic stub nodes for unresolved/external endpoints (default: keep them)")),
			mcp.WithString("scope", mcp.Description("(mermaid) architecture (default) | communities | processes | all")),
			mcp.WithNumber("min_community", mcp.Description("(mermaid) minimum community size (default: 3)")),
			mcp.WithNumber("max_communities", mcp.Description("(mermaid) maximum communities (default: 20)")),
		),
		s.handleExportGraph,
	)
}

func (s *Server) handleExportGraph(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	g := s.graph
	if g == nil {
		return mcp.NewToolResultError("export: graph is not initialised"), nil
	}
	args := req.GetArguments()
	format := strings.ToLower(stringArgOrDefault(args, "format", "cypher"))

	opts := exporter.Options{
		Repo:          stringArg(args, "repo"),
		Languages:     splitCSVArg(stringArg(args, "languages")),
		DropSynthetic: boolArgValue(args, "no_synthetic"),
	}
	for _, k := range splitCSVArg(stringArg(args, "kinds")) {
		opts.Kinds = append(opts.Kinds, graph.NodeKind(strings.ToLower(k)))
	}
	mermaidOpts := exporter.MermaidOpts{
		Scope:          stringArgOrDefault(args, "scope", "architecture"),
		MaxCommunities: intArgOrDefault(args, "max_communities", 20),
		MinCommunity:   intArgOrDefault(args, "min_community", 3),
		Kinds:          opts.Kinds,
		Languages:      opts.Languages,
	}

	outputPath := stringArg(args, "output_path")
	outputDir := stringArg(args, "output_dir")

	// Mermaid multi-file: one file per scope under output_dir.
	if format == "mermaid" && outputDir != "" {
		scopes := []string{"architecture", "communities", "processes"}
		if mermaidOpts.Scope != "all" {
			scopes = []string{mermaidOpts.Scope}
		}
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("mkdir out-dir: %v", err)), nil
		}
		var total exporter.Stats
		for _, sc := range scopes {
			scopeOpts := mermaidOpts
			scopeOpts.Scope = sc
			p := filepath.Join(outputDir, sc+".mermaid")
			f, err := os.Create(p)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("create %q: %v", p, err)), nil
			}
			st, werr := exporter.WriteMermaid(f, g, scopeOpts)
			_ = f.Close()
			if werr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("write mermaid %s: %v", sc, werr)), nil
			}
			total.NodesWritten += st.NodesWritten
			total.EdgesWritten += st.EdgesWritten
			total.BytesWritten += st.BytesWritten
		}
		return s.respondJSONOrTOON(ctx, req, map[string]any{
			"output_dir": outputDir,
			"files":      len(scopes),
			"nodes":      total.NodesWritten,
			"edges":      total.EdgesWritten,
			"bytes":      total.BytesWritten,
		})
	}

	var buf bytes.Buffer
	st, err := exportByFormat(&buf, g, format, opts, mermaidOpts)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if outputPath != "" {
		if err := os.WriteFile(outputPath, buf.Bytes(), 0o644); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("write %q: %v", outputPath, err)), nil
		}
		return s.respondJSONOrTOON(ctx, req, map[string]any{
			"output_path": outputPath,
			"nodes":       st.NodesWritten,
			"edges":       st.EdgesWritten,
			"bytes":       st.BytesWritten,
		})
	}
	// Inline (stdout) — the rendered export is the text content.
	return mcp.NewToolResultText(buf.String()), nil
}



func exportByFormat(w io.Writer, g graph.Store, format string, opts exporter.Options, mermaidOpts exporter.MermaidOpts) (exporter.Stats, error) {
	switch format {
	case "cypher":
		return exporter.WriteCypher(w, g, opts)
	case "graphml":
		return exporter.WriteGraphML(w, g, opts)
	case "mermaid":
		return exporter.WriteMermaid(w, g, mermaidOpts)
	default:
		return exporter.Stats{}, fmt.Errorf("unknown format %q (expected cypher | graphml | mermaid)", format)
	}
}
