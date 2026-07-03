package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIntelephenseInitOptions_StoragePathUnderCacheDir(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	repoRoot := "/some/repo/monolog"

	raw := intelephenseInitOptions(repoRoot)
	require.NotEmpty(t, raw)

	var opts struct {
		StoragePath       string `json:"storagePath"`
		GlobalStoragePath string `json:"globalStoragePath"`
	}
	require.NoError(t, json.Unmarshal(raw, &opts))

	wantBase := filepath.Join(cacheHome, "gortex", "intelephense")
	assert.True(t, strings.HasPrefix(opts.StoragePath, wantBase),
		"storagePath %q should be under %q", opts.StoragePath, wantBase)
	assert.Equal(t, filepath.Join(wantBase, "global"), opts.GlobalStoragePath)
	assert.NotEqual(t, opts.StoragePath, opts.GlobalStoragePath)

	// The per-repo dir is created so intelephense can open it on launch.
	info, err := os.Stat(opts.StoragePath)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestProviderEffectiveInitOptions_AltPrecedence(t *testing.T) {
	// The resolve-time func hook wins when it returns non-empty options.
	pFunc := &Provider{altInitOptionsFunc: func(root string) json.RawMessage {
		return json.RawMessage(`{"from":"func"}`)
	}}
	assert.JSONEq(t, `{"from":"func"}`, string(pFunc.effectiveInitOptions("/repo")))

	// Static per-alternative options win over the spec-level blob.
	pStatic := &Provider{
		altInitOptions: json.RawMessage(`{"from":"alt"}`),
		spec:           &ServerSpec{InitializationOptions: json.RawMessage(`{"from":"spec"}`)},
	}
	assert.JSONEq(t, `{"from":"alt"}`, string(pStatic.effectiveInitOptions("/repo")))

	// Falls back to the spec when the alternative carries nothing.
	pSpec := &Provider{spec: &ServerSpec{InitializationOptions: json.RawMessage(`{"from":"spec"}`)}}
	assert.JSONEq(t, `{"from":"spec"}`, string(pSpec.effectiveInitOptions("/repo")))
}

func TestNewProviderFromSpec_CarriesResolvedAltStoragePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH-based faux executable resolution is POSIX-only")
	}
	// Hermetic PATH: only a faux "intelephense" resolves; the primary
	// "phpactor-not-on-path" is absent, so the alternative must win and
	// carry its InitOptionsFunc onto the Provider.
	binDir := t.TempDir()
	fauxPath := filepath.Join(binDir, "intelephense")
	require.NoError(t, os.WriteFile(fauxPath, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", binDir)
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	spec := &ServerSpec{
		Name:    "phpactor",
		Command: "phpactor-not-on-path",
		Args:    []string{"language-server"},
		AlternativeCommands: []ServerAlt{
			{Command: "intelephense", Args: []string{"--stdio"}, InitOptionsFunc: intelephenseInitOptions},
		},
	}
	p := NewProviderFromSpec(spec, zap.NewNop())
	require.Equal(t, "intelephense", p.command)

	raw := p.effectiveInitOptions("/some/repo/monolog")
	require.NotEmpty(t, raw)
	assert.Contains(t, string(raw), filepath.Join(cacheHome, "gortex", "intelephense"))
	// The spec's default (intelephense's) cache location is never sent as-is:
	// there is no baked storagePath literal on the spec.
	assert.Empty(t, spec.InitializationOptions)
}

func TestPhpactorSpecPinsIntelephenseStorage(t *testing.T) {
	var found bool
	for i := range Servers {
		if Servers[i].Name != "phpactor" {
			continue
		}
		for _, alt := range Servers[i].AlternativeCommands {
			if alt.Command != "intelephense" {
				continue
			}
			found = true
			require.NotNil(t, alt.InitOptionsFunc, "intelephense alt must pin a storagePath")
			assert.Contains(t, string(alt.InitOptionsFunc("/repo")), "storagePath")
		}
	}
	assert.True(t, found, "phpactor spec must list an intelephense alternative")
}
