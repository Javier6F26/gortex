package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// RemoteHealth is the daemon-side view of a remote's GET /v1/health
// advertisement. It mirrors the server's HealthResponse JSON but keeps
// ReadOnly as a *bool so the consumer can distinguish "advertised
// read_only:false" (writable) from "field absent" (an older remote that
// does not advertise) — the latter is treated as read-only (fail-safe).
//
// It lives here, not as a shared type, because internal/daemon must not
// import internal/server (the import direction is server -> daemon).
type RemoteHealth struct {
	Status        string   `json:"status"`
	Indexed       bool     `json:"indexed"`
	Nodes         int      `json:"nodes"`
	Edges         int      `json:"edges"`
	Version       string   `json:"version"`
	UptimeSeconds float64  `json:"uptime_seconds"`
	SchemaVersion int      `json:"schema_version"`
	APIVersion    int      `json:"api_version"`
	ReadOnly      *bool    `json:"read_only"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

// EffectiveReadOnly reports whether the remote must be treated as
// read-only. An unadvertised read_only (nil) is read-only by fail-safe
// default, so an older remote that predates the field is never assumed
// writable.
func (h RemoteHealth) EffectiveReadOnly() bool {
	return h.ReadOnly == nil || *h.ReadOnly
}

// HasCapability reports whether the remote advertised a named federation
// capability (e.g. "subgraph", "events").
func (h RemoteHealth) HasCapability(name string) bool {
	for _, c := range h.Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

// FetchHealth reads the remote's GET /v1/health under the caller's ctx
// (so a slow remote is bounded by the same per-remote deadline the read
// fan-out uses). The bearer token is sent per call, like every other
// remote hop.
func (c *ServerClient) FetchHealth(ctx context.Context) (RemoteHealth, error) {
	var out RemoteHealth
	u, err := url.JoinPath(c.BaseURL, "v1", "health")
	if err != nil {
		return out, fmt.Errorf("join health URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return out, fmt.Errorf("build health request: %w", err)
	}
	if tok := c.resolveAuthToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("fetch health from %q: %w", c.Entry.Slug, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("health from %q: status %d", c.Entry.Slug, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("read health response: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode health from %q: %w", c.Entry.Slug, err)
	}
	return out, nil
}
