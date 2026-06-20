package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var toolsReceiptFormat string

var toolsReceiptCmd = &cobra.Command{
	Use:   "receipt",
	Short: "Print a context-budget receipt: the MCP tool surface the CLI path did NOT mount",
	Long: `Emits an inspectable "context budget receipt" describing the transport and
tool-surface counts the daemon would advertise over MCP. Driving Gortex through
the CLI (or a skill that shells the CLI) mounts no tool schemas into the model's
context, so this receipt is the auditable record of the per-call "tax" avoided.

When a daemon tracks the repo the receipt reports its active preset and the
live / deferred tool counts; when no daemon is reachable it still emits a
receipt recording that no surface was mounted.`,
	RunE: runToolsReceipt,
}

func init() {
	toolsReceiptCmd.Flags().StringVar(&toolsReceiptFormat, "format", "yaml", "output format: yaml or json")
	toolsCmd.AddCommand(toolsReceiptCmd)
}

// toolProfileCounts is the slice of the daemon's tool_profile response the
// receipt reads: the active preset and the live / deferred surface counts.
type toolProfileCounts struct {
	Preset        string   `json:"preset"`
	PresetMode    string   `json:"preset_mode"`
	LiveCount     int      `json:"live_count"`
	DeferredCount int      `json:"deferred_count"`
	Live          []string `json:"live"`
	Deferred      []string `json:"deferred"`
}

// contextBudgetReceipt is the rendered receipt body. Field order is fixed via
// struct order so both the YAML and JSON renderings are deterministic.
type contextBudgetReceipt struct {
	Transport             string                      `json:"transport" yaml:"transport"`
	Repo                  string                      `json:"repo" yaml:"repo"`
	DaemonPreset          string                      `json:"daemon_preset,omitempty" yaml:"daemon_preset,omitempty"`
	DaemonPresetMode      string                      `json:"daemon_preset_mode,omitempty" yaml:"daemon_preset_mode,omitempty"`
	AdvertisedTools       int                         `json:"advertised_tools" yaml:"advertised_tools"`
	RegisteredToolSchemas int                         `json:"registered_tool_schemas" yaml:"registered_tool_schemas"`
	DeferredCapabilities  receiptDeferredCapabilities `json:"deferred_capabilities" yaml:"deferred_capabilities"`
	Decision              string                      `json:"decision" yaml:"decision"`
}

type receiptDeferredCapabilities struct {
	Count int `json:"count" yaml:"count"`
}

// receiptEnvelope wraps the receipt body under the single top-level key both
// renderings carry.
type receiptEnvelope struct {
	GortexContextBudget contextBudgetReceipt `json:"gortex_context_budget" yaml:"gortex_context_budget"`
}

func runToolsReceipt(cmd *cobra.Command, _ []string) error {
	abs, err := filepath.Abs(toolsIndex)
	if err != nil {
		abs = toolsIndex
	}

	receipt := buildContextBudgetReceipt(abs)
	return renderReceipt(cmd, receipt, toolsReceiptFormat)
}

// buildContextBudgetReceipt asks the daemon for its tool_profile and renders
// the receipt. Any error reaching the daemon (no daemon running, repo not
// tracked, or a tool failure) is treated as "no surface mounted" rather than a
// hard failure — the receipt must be emittable even with nothing mounted.
func buildContextBudgetReceipt(absRepo string) contextBudgetReceipt {
	raw, err := toolsDaemonTool(toolsIndex, "tool_profile", map[string]any{})
	if err != nil {
		return contextBudgetReceipt{
			Transport:             "cli_only",
			Repo:                  absRepo,
			AdvertisedTools:       0,
			RegisteredToolSchemas: 0,
			DeferredCapabilities:  receiptDeferredCapabilities{Count: 0},
			Decision:              "no_surface_mounted",
		}
	}

	var counts toolProfileCounts
	_ = json.Unmarshal(raw, &counts)

	// Prefer the explicit *_count fields; fall back to the array lengths so a
	// profile shape that omits the counts still yields a faithful receipt.
	live := counts.LiveCount
	if live == 0 && len(counts.Live) > 0 {
		live = len(counts.Live)
	}
	deferred := counts.DeferredCount
	if deferred == 0 && len(counts.Deferred) > 0 {
		deferred = len(counts.Deferred)
	}

	return contextBudgetReceipt{
		Transport:             "skill_cli",
		Repo:                  absRepo,
		DaemonPreset:          counts.Preset,
		DaemonPresetMode:      counts.PresetMode,
		AdvertisedTools:       live,
		RegisteredToolSchemas: 0,
		DeferredCapabilities:  receiptDeferredCapabilities{Count: deferred},
		Decision:              "full_surface_not_mounted",
	}
}

// renderReceipt writes the receipt as YAML (default) or JSON under the
// gortex_context_budget top-level key.
func renderReceipt(cmd *cobra.Command, receipt contextBudgetReceipt, format string) error {
	out := cmd.OutOrStdout()
	env := receiptEnvelope{GortexContextBudget: receipt}
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(env)
	case "yaml", "":
		b, err := yaml.Marshal(env)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(out, string(b))
		return err
	default:
		return fmt.Errorf("unknown --format %q (want yaml or json)", format)
	}
}
