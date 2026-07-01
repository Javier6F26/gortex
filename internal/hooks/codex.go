package hooks

import (
	"encoding/json"
	"io"
	"os"
)

// RunCodex handles the Codex hook wire shape. The first Codex hook is a
// narrow Bash-only PreToolUse nudge; it deliberately forces ModeEnrich so
// a hand-edited --mode=deny command cannot turn Codex into an enforcing hook.
func RunCodex(port int) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	runCodex(data, port)
}

func runCodex(data []byte, port int) {
	var peek struct {
		HookEventName string `json:"hook_event_name"`
		ToolName      string `json:"tool_name"`
	}
	if err := json.Unmarshal(data, &peek); err != nil {
		return
	}
	if peek.HookEventName != "PreToolUse" || peek.ToolName != "Bash" {
		return
	}
	runPreToolUse(data, port, ModeEnrich)
}
