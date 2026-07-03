package main

import (
	"github.com/zzet/gortex/internal/config"
	"github.com/zzet/gortex/internal/embedding"
	"github.com/zzet/gortex/internal/serverstack"
)

// embedderRequest aliases the shared constructor's request type so the
// cmd entry points keep building it directly; resolveEmbedder is a thin
// wrapper over the relocated logic, exercised by the resolution tests.
type embedderRequest = serverstack.EmbedderRequest

func resolveEmbedder(req embedderRequest, cfg *config.Config) (embedding.Provider, string, error) {
	p, desc, _, err := serverstack.ResolveEmbedder(req, cfg)
	return p, desc, err
}
