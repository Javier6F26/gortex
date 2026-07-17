package serverstack

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/embedding"
	"github.com/zzet/gortex/internal/graph"
	"go.uber.org/zap"
)

// EmbedderRequest carries the explicit, per-invocation embedding inputs a
// command collected from its flags and environment. ResolveEmbedder
// merges these with the on-disk config to decide what provider — if any —
// to construct. Fields are exported so cmd entry points can build the
// request across the package boundary.
type EmbedderRequest struct {
	// FlagChanged reports whether the `--embeddings` boolean flag was
	// explicitly set (cmd.Flags().Changed). Only an explicitly-set flag
	// overrides the config.
	FlagChanged bool
	// FlagEnabled is the value of `--embeddings`. Meaningful only when
	// FlagChanged is true.
	FlagEnabled bool
	// FlagURL / FlagModel are `--embeddings-url` / `--embeddings-model`.
	// A non-empty URL forces the API provider — the most explicit request.
	FlagURL   string
	FlagModel string
	// FlagDims is `--embeddings-dims` (0 = unset). A positive value overrides
	// the startup dimension probe, sizes the vector column, and is forwarded
	// as the `dimensions` request parameter on OpenAI-compatible providers.
	// GORTEX_EMBEDDINGS_DIMS is the env equivalent, honored when the flag is 0.
	FlagDims int
}

// ResolveEmbedder decides which embedding.Provider (if any) to install,
// applying a fixed precedence: an explicit URL (flag or env) forces the
// API provider; an explicit on/off signal (flag or env) decides
// enablement and the provider comes from the `embedding:` config; else
// the config decides (default: semantic search ON with the zero-download
// static GloVe provider). The returned string describes the decision for
// logging ("" when no embedder was built); the SelectionReport records every
// backend the local auto-selection tried (empty for other providers) so a
// silent degradation to static is observable; a non-nil error means an embedder
// was requested but could not be constructed.
func ResolveEmbedder(req EmbedderRequest, cfg *config.Config) (embedding.Provider, string, embedding.SelectionReport, error) {
	if url := firstNonEmpty(req.FlagURL, os.Getenv("GORTEX_EMBEDDINGS_URL")); url != "" {
		model := firstNonEmpty(req.FlagModel, os.Getenv("GORTEX_EMBEDDINGS_MODEL"))
		p := embedding.NewAPIProvider(url, model)
		if dims := resolveDimsOverride(req.FlagDims); dims > 0 {
			p.SetRequestedDimensions(dims)
		}
		return p, fmt.Sprintf("api (%s)", url), embedding.SelectionReport{}, nil
	}

	embCfg := config.EmbeddingConfig{}
	if cfg != nil {
		embCfg = cfg.Embedding
	}

	explicitEnabled, haveExplicit := explicitEmbeddingToggle(req)
	if haveExplicit {
		if !explicitEnabled {
			return nil, "", embedding.SelectionReport{}, nil
		}
		return buildConfiguredEmbedder(embCfg, "enabled by flag/env")
	}

	// No explicit flag/env toggle. An explicit `embedding.enabled: false`
	// still wins and disables the vector channel.
	if embCfg.Enabled != nil && !*embCfg.Enabled {
		return nil, "", embedding.SelectionReport{}, nil
	}
	// An explicit `embedding.enabled: true` builds whatever provider is
	// configured, including the baked static GloVe one.
	if embCfg.Enabled != nil && *embCfg.Enabled {
		return buildConfiguredEmbedder(embCfg, "enabled by config")
	}
	// Otherwise (the default, unset state) build a vector index ONLY when
	// the user has configured a real, model-backed embedder. The static
	// GloVe provider's dim-50 word vectors add little over FTS5/BM25 text
	// search yet cost 0.6-0.7s of every index to build, so it is now
	// opt-in: FTS5 text search serves the default, and semantic search
	// turns on when a `local`/`api` provider (or embedding.enabled: true,
	// or GORTEX_EMBEDDINGS=1) is configured.
	if isRealEmbedder(embCfg.EmbeddingProviderOrDefault()) {
		return buildConfiguredEmbedder(embCfg, "real embedder configured")
	}
	return nil, "", embedding.SelectionReport{}, nil
}

// isRealEmbedder reports whether the named provider is a model-backed
// embedder worth the index-time vector build. The baked `static` GloVe
// provider is excluded: its dim-50 averaged word vectors add little over
// FTS5 text search, so it no longer builds a vector index by default
// (opt in with embedding.enabled: true or GORTEX_EMBEDDINGS=1).
func isRealEmbedder(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "local", "api":
		return true
	default:
		return false
	}
}

// explicitEmbeddingToggle reports whether the caller gave an explicit
// on/off signal for embeddings, and what it was. The flag takes
// precedence over GORTEX_EMBEDDINGS.
func explicitEmbeddingToggle(req EmbedderRequest) (enabled, haveExplicit bool) {
	if req.FlagChanged {
		return req.FlagEnabled, true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GORTEX_EMBEDDINGS"))) {
	case "1", "true", "yes", "on", "y":
		return true, true
	case "0", "false", "no", "off", "n":
		return false, true
	default:
		return false, false
	}
}

// buildConfiguredEmbedder constructs the provider named by the config
// block (defaulting to the static GloVe provider). The returned description
// names the backend actually constructed — "local (hugot)" for an
// auto-selected local backend, or "local → static fallback" when the chain
// degraded — so the startup log tells the truth rather than echoing the
// configured name.
func buildConfiguredEmbedder(embCfg config.EmbeddingConfig, why string) (embedding.Provider, string, embedding.SelectionReport, error) {
	provider := embCfg.EmbeddingProviderOrDefault()
	variant := firstNonEmpty(os.Getenv("GORTEX_EMBEDDINGS_VARIANT"), embCfg.Variant)
	p, report, err := embedding.NewProviderFromConfigWithReport(embedding.ProviderConfig{
		Provider: provider,
		APIURL:   embCfg.APIURL,
		APIModel: embCfg.APIModel,
		Variant:  variant,
	})
	if err != nil {
		return nil, "", report, err
	}
	desc := provider
	switch {
	case variant != "" && provider == "local":
		desc = fmt.Sprintf("%s/%s", provider, variant)
	case provider == "local" && report.Chosen == "static":
		desc = "local → static fallback"
	case provider == "local" && report.Chosen != "":
		desc = fmt.Sprintf("local (%s)", report.Chosen)
	}
	return p, fmt.Sprintf("%s — %s", desc, why), report, nil
}

// EmbeddingSpaceOf derives the embedding-space identity (provider, model,
// dims) of a resolved provider. Providers that cannot name their model (the
// static/local backends) report an empty identity; dimension comparison alone
// then guards them. Dims comes from Dimensions(), which is truthful after the
// startup probe (or immediately for an override / native-width provider).
func EmbeddingSpaceOf(embedder embedding.Provider) graph.EmbeddingSpace {
	sp := graph.EmbeddingSpace{Dims: embedder.Dimensions()}
	if id, ok := embedder.(interface{ EmbeddingSpaceID() (string, string) }); ok {
		sp.Provider, sp.Model = id.EmbeddingSpaceID()
	}
	return sp
}

// bindEmbeddingSpace binds the vector store to the active embedding space and
// reports whether the semantic (vector) channel should stay enabled. It is the
// startup guard for the adaptive-embedding-dimensions contract:
//
//   - Backend without a fixed-width vector column (SQLite): nothing to bind,
//     semantic search stays on.
//   - Width still unknown (probe failed, lazy provider): skip binding now; the
//     indexer's per-batch width guard still protects the column. Semantic on.
//   - Writer: EnsureVectorSpace creates/validates the column. A genuine space
//     mismatch is logged loudly and DISABLES the semantic channel (structural
//     serving continues) until an operator runs the reset — strictly better
//     than every vector upsert failing with SQLSTATE 22000 and silently losing
//     the corpus.
//   - Read-only follower: a foreign space degrades semantic search to BM25,
//     because a mismatched-width query vector is rejected by pgvector outright.
func bindEmbeddingSpace(g graph.Store, embedder embedding.Provider, follow bool, logger *zap.Logger) bool {
	vsm, ok := g.(graph.VectorSpaceManager)
	if !ok {
		return true
	}
	want := EmbeddingSpaceOf(embedder)
	if want.Dims <= 0 {
		return true
	}
	if follow {
		stored, present, err := vsm.ReadEmbeddingSpace()
		if err != nil {
			logger.Warn("serverstack: follower could not read embedding space — semantic search left on",
				zap.Error(err))
			return true
		}
		if present && !stored.Compatible(want) {
			logger.Warn("serverstack: follower embedding space differs from writer — semantic search degraded to BM25",
				zap.String("stored_provider", stored.Provider), zap.String("stored_model", stored.Model), zap.Int("stored_dims", stored.Dims),
				zap.String("follower_provider", want.Provider), zap.String("follower_model", want.Model), zap.Int("follower_dims", want.Dims))
			return false
		}
		return true
	}
	if err := vsm.EnsureVectorSpace(want); err != nil {
		logger.Error("serverstack: embedding-space bind failed — semantic search disabled until reset",
			zap.Error(err))
		return false
	}
	return true
}

// EmbeddingChunkOptions translates the chunking knobs of an
// EmbeddingConfig into the embedding package's ChunkOptions. Zero values
// pass through — the chunker substitutes its own defaults.
func EmbeddingChunkOptions(cfg *config.Config) embedding.ChunkOptions {
	if cfg == nil {
		return embedding.ChunkOptions{}
	}
	return embedding.ChunkOptions{
		ThresholdLines: cfg.Embedding.ChunkThresholdLines,
		WindowLines:    cfg.Embedding.ChunkWindowLines,
	}
}

// resolveDimsOverride returns the requested embedding dimensionality,
// preferring an explicit positive flag over GORTEX_EMBEDDINGS_DIMS. A
// non-numeric or non-positive env value is ignored (returns 0), leaving the
// startup probe as the source of truth.
func resolveDimsOverride(flagDims int) int {
	if flagDims > 0 {
		return flagDims
	}
	if v := strings.TrimSpace(os.Getenv("GORTEX_EMBEDDINGS_DIMS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// firstNonEmpty returns the first non-empty string argument.
func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
