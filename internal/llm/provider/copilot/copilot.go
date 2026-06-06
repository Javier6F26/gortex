// Package copilot is the GitHub Copilot CLI llm.Provider.
//
// It shells out to the user's locally installed `copilot` binary in
// non-interactive (`-p`) mode, reusing the existing GitHub Copilot /
// `gh` sign-in — no API key in gortex's environment. The shared
// agentcli engine handles spawning, timeout, capture, and structured
// extraction; this package only describes how to invoke `copilot`.
package copilot

import (
	"strings"
	"time"

	"github.com/zzet/gortex/internal/llm"
	"github.com/zzet/gortex/internal/llm/provider/agentcli"
)

// New constructs the Copilot CLI provider, verifying the binary is on
// $PATH. The prompt is passed via `-p` and the model via `--model`.
func New(cfg llm.CLIConfig) (llm.Provider, error) {
	return agentcli.New(agentcli.Spec{
		ProviderID: "copilot",
		Model:      strings.TrimSpace(cfg.Model),
		ModelFlag:  "--model",
		Delivery:   agentcli.DeliveryFlag,
		PromptFlag: "-p",
		ExtraArgs:  append([]string(nil), cfg.Args...),
		Timeout:    time.Duration(cfg.TimeoutSeconds) * time.Second,
	}, cfg.Binary, "copilot")
}
