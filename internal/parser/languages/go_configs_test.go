package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestGoConfigs_ViperGetReadEdge(t *testing.T) {
	src := `package foo

type Viper struct{}

func (v *Viper) GetString(key string) string { return "" }
func (v *Viper) GetInt(key string) int       { return 0 }

func Run(v *Viper) {
	_ = v.GetString("server.port")
	_ = v.GetInt("server.timeout")
}
`
	fix := runGoExtract(t, src)

	keys := fix.nodesByKind[graph.KindConfigKey]
	if len(keys) != 2 {
		t.Fatalf("expected 2 KindConfigKey, got %d: %+v", len(keys), keys)
	}

	gotIDs := map[string]bool{}
	for _, k := range keys {
		gotIDs[k.ID] = true
		if s, _ := k.Meta["source"].(string); s != "viper" {
			t.Errorf("source = %q", s)
		}
	}
	if !gotIDs["cfg::viper::server.port"] || !gotIDs["cfg::viper::server.timeout"] {
		t.Errorf("missing key IDs: %v", gotIDs)
	}

	reads := fix.edgesByKind[graph.EdgeReadsConfig]
	if len(reads) != 2 {
		t.Errorf("expected 2 EdgeReadsConfig, got %d", len(reads))
	}
	for _, e := range reads {
		if op, _ := e.Meta["op"].(string); op != "read" {
			t.Errorf("op meta = %q", op)
		}
	}
}

func TestGoConfigs_SetEmitsWriteEdge(t *testing.T) {
	src := `package foo

type Viper struct{}

func (v *Viper) Set(key string, val any) {}
func (v *Viper) SetDefault(key string, val any) {}

func Run(v *Viper) {
	v.Set("server.port", 8080)
	v.SetDefault("server.timeout", 30)
}
`
	fix := runGoExtract(t, src)

	writes := fix.edgesByKind[graph.EdgeWritesConfig]
	if len(writes) != 2 {
		t.Fatalf("expected 2 EdgeWritesConfig, got %d", len(writes))
	}
	for _, e := range writes {
		if op, _ := e.Meta["op"].(string); op != "write" {
			t.Errorf("op = %q", op)
		}
	}
	// No reads should fire on this fixture.
	if got := len(fix.edgesByKind[graph.EdgeReadsConfig]); got != 0 {
		t.Errorf("expected 0 reads, got %d", got)
	}
}

func TestGoConfigs_BindEnvEmitsRegister(t *testing.T) {
	src := `package foo

type Viper struct{}

func (v *Viper) BindEnv(key string) error { return nil }

func Run(v *Viper) {
	_ = v.BindEnv("server.port")
}
`
	fix := runGoExtract(t, src)

	// Register operations route through reads-config (the spec
	// classifies BindEnv as "register" but it surfaces as an edge
	// kind that read-side queries pick up).
	reads := fix.edgesByKind[graph.EdgeReadsConfig]
	if len(reads) != 1 {
		t.Fatalf("expected 1 read-style edge for BindEnv, got %d", len(reads))
	}
	if op, _ := reads[0].Meta["op"].(string); op != "register" {
		t.Errorf("BindEnv op meta should be 'register', got %q", op)
	}
}

func TestGoConfigs_DynamicKeySkipped(t *testing.T) {
	src := `package foo

type Viper struct{}

func (v *Viper) GetString(key string) string { return "" }

func Run(v *Viper, dynamic string) {
	_ = v.GetString(dynamic)
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.nodesByKind[graph.KindConfigKey]); got != 0 {
		t.Errorf("dynamic key should not produce KindConfigKey, got %d", got)
	}
}

func TestGoConfigs_NonViperGetIgnored(t *testing.T) {
	// `Cache.Get` shares the bare method name 'Get' but is not in
	// the viper allowlist (only typed `Get*` plus exact `Get`).
	// The viper allowlist DOES include exact `Get`, so a `Cache`
	// type's `Get` would currently match — this is an acknowledged
	// false-positive trade-off when the user opts into the configs
	// gate. The gate stays default-off for this reason. This test
	// documents the behaviour: bare `Cache.Get("key")` matches.
	src := `package foo

type Cache struct{}

func (c *Cache) Get(key string) any { return nil }

func Run(c *Cache) {
	_ = c.Get("user_123")
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.nodesByKind[graph.KindConfigKey]); got != 1 {
		t.Errorf("acknowledged false positive: bare Get should currently match, got %d", got)
	}
}

func TestGoConfigs_UnrelatedTypedGetIgnored(t *testing.T) {
	// `Cache.GetItem` does NOT match because GetItem isn't in the
	// viper allowlist — the strict enumeration is the false-
	// positive guard. This test pins the strictness.
	src := `package foo

type Cache struct{}

func (c *Cache) GetItem(key string) any { return nil }

func Run(c *Cache) {
	_ = c.GetItem("user_123")
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.nodesByKind[graph.KindConfigKey]); got != 0 {
		t.Errorf("Cache.GetItem must not match — strict allowlist is the guard, got %d", got)
	}
}

func TestGoConfigs_DuplicateKeyDeduplicates(t *testing.T) {
	src := `package foo

type Viper struct{}

func (v *Viper) GetString(key string) string { return "" }

func A(v *Viper) { _ = v.GetString("server.port") }
func B(v *Viper) { _ = v.GetString("server.port") }
`
	fix := runGoExtract(t, src)
	keys := fix.nodesByKind[graph.KindConfigKey]
	if len(keys) != 1 {
		t.Errorf("expected 1 deduped key node, got %d", len(keys))
	}
	if got := len(fix.edgesByKind[graph.EdgeReadsConfig]); got != 2 {
		t.Errorf("expected 2 read edges (one per call site), got %d", got)
	}
}
