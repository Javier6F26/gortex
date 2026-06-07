package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServerEntry_EnabledAbsentMeansEnabled asserts the pointer-bool
// backward-compat contract: a roster written before the `enabled` key
// existed (no key at all) loads as enabled, never silently disabled.
func TestServerEntry_EnabledAbsentMeansEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	// No `enabled` key on the entry — the pre-existing-roster shape.
	roster := `[[server]]
slug = "r2"
url = "https://r2.example:4747"
`
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Server) != 1 {
		t.Fatalf("want 1 server, got %d", len(cfg.Server))
	}
	if cfg.Server[0].Enabled != nil {
		t.Fatalf("absent key should unmarshal to nil Enabled, got %v", *cfg.Server[0].Enabled)
	}
	if !cfg.Server[0].IsEnabled() {
		t.Fatal("absent enabled key must be treated as enabled (default-on)")
	}
}

// TestServerEntry_EnabledFalsePersists asserts an explicit
// `enabled = false` round-trips and reports disabled.
func TestServerEntry_EnabledFalsePersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	roster := `[[server]]
slug = "r2"
url = "https://r2.example:4747"
enabled = false
`
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server[0].Enabled == nil || *cfg.Server[0].Enabled {
		t.Fatalf("explicit enabled=false should unmarshal to a non-nil false, got %v", cfg.Server[0].Enabled)
	}
	if cfg.Server[0].IsEnabled() {
		t.Fatal("explicit enabled=false must report disabled")
	}
}

// TestSetEnabled_OnDeletesKey asserts `proxy on` semantics: SetEnabled
// to true CLEARS the key (back to default-on) rather than writing
// `enabled = true`, so a re-saved roster stays minimal.
func TestSetEnabled_OnDeletesKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	roster := `[[server]]
slug = "r2"
url = "https://r2.example:4747"
enabled = false
`
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	changed, err := cfg.SetEnabled("r2", true)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !changed {
		t.Fatal("flipping false->on should report changed")
	}
	if cfg.Server[0].Enabled != nil {
		t.Fatal("SetEnabled(on) must clear the key (nil), not write enabled=true")
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "enabled") {
		t.Fatalf("saved roster must not carry an enabled key after proxy-on, got:\n%s", data)
	}
	// And it reloads as enabled.
	reloaded, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.Server[0].IsEnabled() {
		t.Fatal("re-saved + reloaded roster must be enabled")
	}
}

// TestSetEnabled_OffWritesFalse asserts `proxy off` persists an
// explicit false that survives a daemon restart (Save + reload).
func TestSetEnabled_OffWritesFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	roster := `[[server]]
slug = "r2"
url = "https://r2.example:4747"
`
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	changed, err := cfg.SetEnabled("r2", false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !changed {
		t.Fatal("flipping default-on->off should report changed")
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := LoadServersConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Server[0].IsEnabled() {
		t.Fatal("proxy off must survive a restart (reload) as disabled")
	}
}

// TestSetEnabled_UnknownSlug asserts SetEnabled errors on a slug not in
// the roster rather than silently no-op'ing.
func TestSetEnabled_UnknownSlug(t *testing.T) {
	cfg := &ServersConfig{Server: []ServerEntry{{Slug: "r2", URL: "https://r2.example:4747"}}}
	if _, err := cfg.SetEnabled("nope", false); err == nil {
		t.Fatal("SetEnabled on an unknown slug must error")
	}
}

// TestSetEnabled_Idempotent asserts a no-op toggle reports changed=false.
func TestSetEnabled_Idempotent(t *testing.T) {
	cfg := &ServersConfig{Server: []ServerEntry{{Slug: "r2", URL: "https://r2.example:4747"}}}
	changed, err := cfg.SetEnabled("r2", true) // already default-on
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("enabling an already-enabled remote should report no change")
	}
}
