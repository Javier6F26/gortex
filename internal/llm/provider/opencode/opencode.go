// Package opencode is the opencode CLI llm.Provider.
//
// It shells out to the user's locally installed `opencode` binary in
// non-interactive mode (`opencode run`), reusing the existing opencode
// credentials — no API key in gortex's environment. The prompt is the
// trailing positional argument and the model uses opencode's
// `provider/model` form (e.g. "anthropic/claude-sonnet-4-6"). The
// shared agentcli engine handles spawning, timeout, capture, and
// structured extraction.
package opencode

import (
	"strings"
	"time"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/provider/agentcli"
)

// New constructs the opencode CLI provider, verifying the binary is on
// $PATH. The prompt is the trailing positional after `run`; the model
// is passed via `--model`.
func New(cfg llm.CLIConfig) (llm.Provider, error) {
	return agentcli.New(agentcli.Spec{
		ProviderID: "opencode",
		Model:      strings.TrimSpace(cfg.Model),
		ModelFlag:  "--model",
		BaseArgs:   []string{"run"},
		Delivery:   agentcli.DeliveryArg,
		ExtraArgs:  append([]string(nil), cfg.Args...),
		Timeout:    time.Duration(cfg.TimeoutSeconds) * time.Second,
	}, cfg.Binary, "opencode")
}
