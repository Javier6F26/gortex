// Package cursor is the Cursor Agent CLI llm.Provider.
//
// It shells out to the user's locally installed `cursor-agent` binary
// in non-interactive print mode (`-p`), reusing the existing Cursor
// sign-in — no API key in gortex's environment. `--output-format text`
// keeps stdout to the model's answer. The shared agentcli engine
// handles spawning, timeout, capture, and structured extraction.
package cursor

import (
	"strings"
	"time"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/provider/agentcli"
)

// New constructs the Cursor Agent CLI provider, verifying the binary is
// on $PATH. The prompt is passed via `-p`, the model via `--model`, and
// output is forced to plain text.
func New(cfg llm.CLIConfig) (llm.Provider, error) {
	return agentcli.New(agentcli.Spec{
		ProviderID: "cursor",
		Model:      strings.TrimSpace(cfg.Model),
		ModelFlag:  "--model",
		BaseArgs:   []string{"--output-format", "text"},
		Delivery:   agentcli.DeliveryFlag,
		PromptFlag: "-p",
		ExtraArgs:  append([]string(nil), cfg.Args...),
		Timeout:    time.Duration(cfg.TimeoutSeconds) * time.Second,
	}, cfg.Binary, "cursor-agent")
}
