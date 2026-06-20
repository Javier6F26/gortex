package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// cannedToolProfileCountsJSON mirrors the full-mode tool_profile response shape
// the daemon returns: top-level preset / preset_mode / live_count /
// deferred_count fields (handleToolProfile).
const cannedToolProfileCountsJSON = `{
  "lazy_enabled": true,
  "preset": "core",
  "preset_mode": "defer",
  "total": 175,
  "live_count": 34,
  "deferred_count": 141,
  "live": ["search_symbols", "get_callers"],
  "deferred": ["edit_file", "rename_symbol"]
}`

// newToolsReceiptTestCmd resets receipt flag state and binds a buffer.
func newToolsReceiptTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	toolsIndex = "."
	toolsReceiptFormat = "yaml"

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{Use: "receipt", RunE: runToolsReceipt}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

// TestToolsReceipt_DaemonReachable_YAML asserts a daemon-reachable receipt
// reports skill_cli transport and the live / deferred counts from tool_profile.
func TestToolsReceipt_DaemonReachable_YAML(t *testing.T) {
	orig := toolsDaemonTool
	t.Cleanup(func() { toolsDaemonTool = orig })

	var gotTool string
	toolsDaemonTool = func(_ string, tool string, _ map[string]any) (json.RawMessage, error) {
		gotTool = tool
		return json.RawMessage(cannedToolProfileCountsJSON), nil
	}

	cmd, buf := newToolsReceiptTestCmd(t)
	require.NoError(t, runToolsReceipt(cmd, nil))
	require.Equal(t, "tool_profile", gotTool)

	var env receiptEnvelope
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &env))
	r := env.GortexContextBudget
	require.Equal(t, "skill_cli", r.Transport)
	require.Equal(t, "core", r.DaemonPreset)
	require.Equal(t, "defer", r.DaemonPresetMode)
	require.Equal(t, 34, r.AdvertisedTools)
	require.Equal(t, 0, r.RegisteredToolSchemas)
	require.Equal(t, 141, r.DeferredCapabilities.Count)
	require.Equal(t, "full_surface_not_mounted", r.Decision)
	// repo is the absolute path of the index.
	require.NotEmpty(t, r.Repo)

	// The top-level key is present in the raw YAML text.
	require.Contains(t, buf.String(), "gortex_context_budget:")
}

// TestToolsReceipt_DaemonReachable_JSON asserts the JSON rendering carries the
// same structure under the gortex_context_budget key.
func TestToolsReceipt_DaemonReachable_JSON(t *testing.T) {
	orig := toolsDaemonTool
	t.Cleanup(func() { toolsDaemonTool = orig })
	toolsDaemonTool = func(string, string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(cannedToolProfileCountsJSON), nil
	}

	cmd, buf := newToolsReceiptTestCmd(t)
	toolsReceiptFormat = "json"
	require.NoError(t, runToolsReceipt(cmd, nil))

	var env receiptEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	r := env.GortexContextBudget
	require.Equal(t, "skill_cli", r.Transport)
	require.Equal(t, "core", r.DaemonPreset)
	require.Equal(t, "defer", r.DaemonPresetMode)
	require.Equal(t, 34, r.AdvertisedTools)
	require.Equal(t, 0, r.RegisteredToolSchemas)
	require.Equal(t, 141, r.DeferredCapabilities.Count)
	require.Equal(t, "full_surface_not_mounted", r.Decision)
}

// TestToolsReceipt_NoDaemon asserts that when the daemon call fails, the receipt
// degrades to cli_only / no_surface_mounted without a non-zero error exit.
func TestToolsReceipt_NoDaemon(t *testing.T) {
	orig := toolsDaemonTool
	t.Cleanup(func() { toolsDaemonTool = orig })
	toolsDaemonTool = func(string, string, map[string]any) (json.RawMessage, error) {
		// The shape of the no-daemon signal the real path returns.
		return nil, errors.New("no gortex daemon is running — start it with `gortex daemon start --detach`")
	}

	cmd, buf := newToolsReceiptTestCmd(t)
	require.NoError(t, runToolsReceipt(cmd, nil))

	var env receiptEnvelope
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &env))
	r := env.GortexContextBudget
	require.Equal(t, "cli_only", r.Transport)
	require.Equal(t, 0, r.AdvertisedTools)
	require.Equal(t, 0, r.RegisteredToolSchemas)
	require.Equal(t, 0, r.DeferredCapabilities.Count)
	require.Equal(t, "no_surface_mounted", r.Decision)
	// daemon preset / mode are omitted when nothing is mounted.
	require.Empty(t, r.DaemonPreset)
	require.Empty(t, r.DaemonPresetMode)
}

// TestToolsReceipt_CountFallback asserts a profile that omits the *_count
// fields still yields faithful counts from the live / deferred arrays.
func TestToolsReceipt_CountFallback(t *testing.T) {
	orig := toolsDaemonTool
	t.Cleanup(func() { toolsDaemonTool = orig })
	toolsDaemonTool = func(string, string, map[string]any) (json.RawMessage, error) {
		// No live_count / deferred_count — only the arrays.
		return json.RawMessage(`{"preset":"nav","preset_mode":"hide","live":["a","b","c"],"deferred":["d"]}`), nil
	}

	cmd, buf := newToolsReceiptTestCmd(t)
	require.NoError(t, runToolsReceipt(cmd, nil))

	var env receiptEnvelope
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &env))
	r := env.GortexContextBudget
	require.Equal(t, "skill_cli", r.Transport)
	require.Equal(t, 3, r.AdvertisedTools)
	require.Equal(t, 1, r.DeferredCapabilities.Count)
}

// TestToolsReceipt_BadFormat asserts an unknown --format value is a clean error.
func TestToolsReceipt_BadFormat(t *testing.T) {
	orig := toolsDaemonTool
	t.Cleanup(func() { toolsDaemonTool = orig })
	toolsDaemonTool = func(string, string, map[string]any) (json.RawMessage, error) {
		return json.RawMessage(cannedToolProfileCountsJSON), nil
	}

	cmd, _ := newToolsReceiptTestCmd(t)
	toolsReceiptFormat = "toml"
	err := runToolsReceipt(cmd, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown --format")
}
