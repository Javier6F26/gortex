package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_sqlite"
	"github.com/zzet/gortex/internal/parser"
	"github.com/zzet/gortex/internal/parser/languages"
)

// TestContentSplit_StreamedToIndexAndLeaned verifies the C1 contract: a
// content file's section bodies are streamed into the dedicated content
// index (searchable, full text) while the graph nodes are leaned to a
// capped snippet. Proven by a unique marker placed BEYOND the snippet cap:
// it must be findable via the content index but absent from the node.
// Run on both the shadow path (generous byte budget) and the non-shadow
// disk path (tiny budget forces Fix B's per-call route).
func TestContentSplit_StreamedToIndexAndLeaned(t *testing.T) {
	for _, tc := range []struct{ name, budget string }{
		{"shadow_path", "1073741824"}, // 1 GiB — the tiny repo takes the in-memory shadow
		{"disk_path", "1"},            // 1 byte — forces the bounded non-shadow disk path
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GORTEX_SHADOW_MAX_BYTES", tc.budget)
			dir := t.TempDir()
			// One section (< 4000 chars): a head marker inside the snippet,
			// filler, then a tail marker past the 240-byte snippet cap.
			body := "zzheadmarker " + strings.Repeat("filler word ", 60) + " zztailmarker"
			writeFile(t, filepath.Join(dir, "doc.txt"), body)

			store, err := store_sqlite.Open(filepath.Join(t.TempDir(), "store.sqlite"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })

			reg := parser.NewRegistry()
			languages.RegisterAll(reg)
			cfg := config.Default().Index
			cfg.Workers = 2
			_, err = New(store, reg, cfg, zap.NewNop()).IndexCtx(context.Background(), dir)
			require.NoError(t, err)

			// The full body is searchable via the content index — including
			// the tail marker that lives only past the node snippet, proving
			// the index holds the whole body, not just the snippet.
			hits, err := store.SearchContent("zztailmarker", "", 10)
			require.NoError(t, err)
			require.NotEmpty(t, hits, "full content body must be searchable via the content index")
			require.Contains(t, hits[0].NodeID, "doc.txt")

			// The content node is lean: section_text is a capped snippet, the
			// tail marker is gone from the node, and it is marked indexed.
			var contentNodes int
			for _, n := range store.AllNodes() {
				if !isContentNode(n) {
					continue
				}
				contentNodes++
				st, _ := n.Meta["section_text"].(string)
				require.LessOrEqual(t, len(st), contentSnippetCap,
					"content node section_text must be trimmed to the snippet cap")
				require.NotContains(t, st, "zztailmarker",
					"the full body must not remain on the node")
				require.Equal(t, true, n.Meta["content_indexed"],
					"content node must be marked content_indexed")
			}
			require.Positive(t, contentNodes, "expected at least one content section node")

			_ = graph.ContentSearcher(store) // store satisfies the capability
		})
	}
}
