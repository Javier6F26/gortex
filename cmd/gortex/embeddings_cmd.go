package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/daemon"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/graph/store_pg"
	"github.com/zzet/gortex/internal/serverstack"
)

var (
	embeddingsResetPGDSN string
	embeddingsResetURL   string
	embeddingsResetModel string
	embeddingsResetDims  int
	embeddingsResetYes   bool
)

var embeddingsCmd = &cobra.Command{
	Use:   "embeddings",
	Short: "Manage the embedding-space contract of the PostgreSQL vector store",
	Long: `Inspect and re-bind the embedding space (provider, model, dimensions) the
PostgreSQL vector store is bound to.

The vector column dimension follows the active embedding provider (in-process
50, Ollama 768, OpenAI 1536, or a reduced-dimension override). Switching to a
provider with a different dimensionality requires an explicit reset — vectors
from one space are not comparable to another, and the column cannot change
dimension in place.`,
}

var embeddingsResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Drop the vector data and re-bind the store to the configured embedding provider",
	Long: `Reset drops the vectors table and the embedding-space record, then recreates
them sized for the CURRENTLY configured embedding provider. Structural graph
data (nodes, edges, blobs) is untouched.

Use this after deliberately switching embedding providers or models. The next
index pass re-embeds the corpus — for a large workspace this is a paid,
time-consuming operation (every symbol is re-sent to the provider).

Requires the postgres backend. Refuses to run while a writer daemon holds the
schema lock — stop it first (gortex daemon stop).`,
	RunE: runEmbeddingsReset,
}

func init() {
	embeddingsResetCmd.Flags().StringVar(&embeddingsResetPGDSN, "pg-dsn", "",
		"PostgreSQL DSN (default: $GORTEX_PG_DSN, else auto-detect from a running daemon)")
	embeddingsResetCmd.Flags().StringVar(&embeddingsResetURL, "embeddings-url", "",
		"OpenAI-compatible (or Ollama) embedding API base URL used to size the new space (else the embedding: config / $GORTEX_EMBEDDINGS_URL)")
	embeddingsResetCmd.Flags().StringVar(&embeddingsResetModel, "embeddings-model", "",
		"embedding model for --embeddings-url")
	embeddingsResetCmd.Flags().IntVar(&embeddingsResetDims, "embeddings-dims", 0,
		"override the dimension probe (also $GORTEX_EMBEDDINGS_DIMS): size the new column to this width")
	embeddingsResetCmd.Flags().BoolVar(&embeddingsResetYes, "yes", false,
		"skip the confirmation prompt")

	embeddingsCmd.AddCommand(embeddingsResetCmd)
	rootCmd.AddCommand(embeddingsCmd)
}

func runEmbeddingsReset(cmd *cobra.Command, _ []string) error {
	// Resolve the DSN: flag > env > running daemon. Reset only applies to the
	// postgres backend (the SQLite blob store has no fixed-width column).
	dsn := embeddingsResetPGDSN
	if dsn == "" {
		dsn = os.Getenv("GORTEX_PG_DSN")
	}
	if dsn == "" {
		dsn = detectDaemonPGDSN()
	}
	if dsn == "" {
		return fmt.Errorf("embeddings reset requires the postgres backend: pass --pg-dsn, set $GORTEX_PG_DSN, or start the daemon with --backend postgres")
	}

	// A running writer daemon holds the schema/writer lock; ResetVectorSpace
	// would refuse. Fail early with a clear instruction instead.
	if daemon.IsRunning() {
		return fmt.Errorf("a gortex daemon is running and holds the writer lock — stop it first with `gortex daemon stop`, then re-run `gortex embeddings reset`")
	}

	// Resolve the target embedding space from the currently configured
	// provider (flags > env > config). This is what the new column is sized to.
	cfg, err := config.Load(cfgFile)
	if err != nil {
		cfg = nil // ResolveEmbedder tolerates a nil config (flags/env only)
	}
	req := serverstack.EmbedderRequest{
		FlagURL:   embeddingsResetURL,
		FlagModel: embeddingsResetModel,
		FlagDims:  embeddingsResetDims,
	}
	embedder, embDesc, _, err := serverstack.ResolveEmbedder(req, cfg)
	if err != nil {
		return fmt.Errorf("resolve embedding provider: %w", err)
	}
	if embedder == nil {
		return fmt.Errorf("no embedding provider is configured — nothing to bind (configure one via --embeddings-url or the embedding: config block)")
	}
	defer embedder.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Probe the provider so Dimensions() is truthful before we size the column.
	// An override (--embeddings-dims) already seeds it, so the probe short-circuits.
	if prober, ok := embedder.(interface {
		ProbeDimensions(context.Context) (int, error)
	}); ok {
		if _, perr := prober.ProbeDimensions(ctx); perr != nil {
			return fmt.Errorf("probe embedding provider (%s): %w — cannot size the vector column", embDesc, perr)
		}
	}
	want := serverstack.EmbeddingSpaceOf(embedder)
	if want.Dims <= 0 {
		return fmt.Errorf("embedding provider (%s) reported an unknown dimension — pass --embeddings-dims to size the column explicitly", embDesc)
	}

	if !embeddingsResetYes {
		fmt.Printf("This will DROP all stored vectors and re-bind the store to {provider=%q model=%q dims=%d}.\n",
			want.Provider, want.Model, want.Dims)
		fmt.Printf("Structural graph data is preserved. The next index pass re-embeds the whole corpus (a paid, time-consuming operation).\n")
		fmt.Print("Proceed? [y/N]: ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" && answer != "yes" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	st, err := store_pg.Open(ctx, store_pg.Config{DSN: dsn})
	if err != nil {
		return fmt.Errorf("open postgres store: %w", err)
	}
	defer st.Close()

	vsm, ok := any(st).(graph.VectorSpaceManager)
	if !ok {
		return fmt.Errorf("this store does not support embedding-space management")
	}
	if err := vsm.ResetVectorSpace(want); err != nil {
		return fmt.Errorf("reset embedding space: %w", err)
	}

	fmt.Printf("Embedding space reset: vectors recreated as vector(%d), bound to {provider=%q model=%q}.\n",
		want.Dims, want.Provider, want.Model)
	fmt.Println("Re-embed the corpus by running an index pass (the daemon does this automatically on next start).")
	return nil
}
