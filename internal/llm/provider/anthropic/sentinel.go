package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Claude model sentinels. A user can pin `llm.anthropic.model` to one
// of these tier aliases instead of a dated model id; New then resolves
// it to the newest live model in that tier, so a config never has to be
// edited when Anthropic ships a point release.
const (
	sentinelHaiku  = "claude-haiku"
	sentinelSonnet = "claude-sonnet"
	sentinelOpus   = "claude-opus"
)

// Pinned fallbacks used when live discovery is unavailable (offline, no
// models:list permission, a transient error). They are the newest known
// model in each tier at build time — always a valid, current id.
const (
	fallbackHaiku  = "claude-haiku-4-5-20251001"
	fallbackSonnet = "claude-sonnet-4-6"
	fallbackOpus   = "claude-opus-4-8"
)

type sentinelSpec struct {
	fallback string
	envVar   string
	matches  func(id string) bool
}

// sentinelSpecs maps each sentinel to its per-tier env override, the
// substring that identifies its tier in a model id, and its pinned
// fallback.
var sentinelSpecs = map[string]sentinelSpec{
	sentinelHaiku:  {fallbackHaiku, "GORTEX_LLM_ANTHROPIC_HAIKU_MODEL", func(id string) bool { return strings.Contains(strings.ToLower(id), "haiku") }},
	sentinelSonnet: {fallbackSonnet, "GORTEX_LLM_ANTHROPIC_SONNET_MODEL", func(id string) bool { return strings.Contains(strings.ToLower(id), "sonnet") }},
	sentinelOpus:   {fallbackOpus, "GORTEX_LLM_ANTHROPIC_OPUS_MODEL", func(id string) bool { return strings.Contains(strings.ToLower(id), "opus") }},
}

// Resolution is cached per (sentinel, auth identity) for the process
// lifetime: a given key resolves a tier to a live model id exactly once.
var (
	modelCacheMu sync.Mutex
	modelCache   = map[string]string{}
)

// resolveModel turns a sentinel (claude-haiku / claude-sonnet /
// claude-opus) into a concrete model id. A non-sentinel model passes
// through unchanged. Resolution order: a per-tier env override, then a
// cached result, then live discovery via the models endpoint, then the
// pinned fallback. Any failure degrades to the fallback — resolution
// never blocks provider construction.
func resolveModel(model, apiKey, baseURL string, client *http.Client) string {
	name := strings.ToLower(strings.TrimSpace(model))
	spec, ok := sentinelSpecs[name]
	if !ok {
		return model // an explicit, dated model id — use verbatim
	}

	if v := strings.TrimSpace(os.Getenv(spec.envVar)); v != "" {
		return v
	}

	cacheKey := name + "|" + authIdentity(apiKey)
	modelCacheMu.Lock()
	if cached, ok := modelCache[cacheKey]; ok {
		modelCacheMu.Unlock()
		return cached
	}
	modelCacheMu.Unlock()

	resolved := spec.fallback
	if id := discoverLatestModel(spec, apiKey, baseURL, client); id != "" {
		resolved = id
	}

	modelCacheMu.Lock()
	modelCache[cacheKey] = resolved
	modelCacheMu.Unlock()
	return resolved
}

// authIdentity is a stable, non-secret cache key derived from the API
// key — so two keys never share a cached resolution, but the key itself
// is never stored.
func authIdentity(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

// discoverLatestModel lists the account's models and returns the newest
// id in the sentinel's tier (by created_at), or "" on any error.
func discoverLatestModel(spec sentinelSpec, apiKey, baseURL string, client *http.Client) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models?limit=1000", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}

	var parsed struct {
		Data []struct {
			ID        string `json:"id"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ""
	}

	var bestID, bestCreated string
	for _, m := range parsed.Data {
		if !spec.matches(m.ID) {
			continue
		}
		// created_at is RFC3339; lexical order matches chronological
		// order for that format, so a string compare picks the newest.
		if bestID == "" || m.CreatedAt > bestCreated {
			bestID, bestCreated = m.ID, m.CreatedAt
		}
	}
	return bestID
}
