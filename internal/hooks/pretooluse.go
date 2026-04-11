// Package hooks provides Claude Code hook handlers for Gortex.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// HookInput is the JSON structure Claude Code sends to PreToolUse hooks via stdin.
type HookInput struct {
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	CWD           string         `json:"cwd"`
}

// HookOutput is the JSON structure the hook writes to stdout.
type HookOutput struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput carries the additional context to inject.
type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// RunPreToolUse handles a PreToolUse hook invocation.
// It reads from stdin, checks if the tool call can be enriched,
// queries the Gortex web server, and writes enrichment to stdout.
func RunPreToolUse(gortexPort int) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var input HookInput
	if err := json.Unmarshal(data, &input); err != nil {
		return
	}

	if input.HookEventName != "PreToolUse" {
		return
	}

	context := enrich(input, gortexPort)
	if context == "" {
		return
	}

	output := HookOutput{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     "PreToolUse",
			AdditionalContext: context,
		},
	}

	out, err := json.Marshal(output)
	if err != nil {
		return
	}
	fmt.Print(string(out))
}

func enrich(input HookInput, port int) string {
	switch input.ToolName {
	case "Read":
		return enrichRead(input.ToolInput, port)
	case "Grep":
		return enrichGrep(input.ToolInput, port)
	case "Glob":
		return enrichGlob(input.ToolInput)
	default:
		return ""
	}
}

// enrichRead calls get_file_summary for the file being read, and suggests graph alternatives.
func enrichRead(toolInput map[string]any, port int) string {
	filePath, ok := toolInput["file_path"].(string)
	if !ok || filePath == "" {
		return ""
	}

	// Skip non-source files.
	if !looksLikeSourceFile(filePath) {
		return ""
	}

	var guidance strings.Builder
	guidance.WriteString("[Gortex] PREFER graph tools over Read for source files:\n")
	guidance.WriteString("  - To read one symbol: use `get_symbol_source` (80% fewer tokens)\n")
	guidance.WriteString("  - To understand a file before editing: use `get_editing_context`\n")
	guidance.WriteString("  - To get a file overview: use `get_file_summary`\n")
	guidance.WriteString("  - For task-level context: use `smart_context`\n")

	resp, err := queryGortex(port, "/api/graph/file?path="+url.QueryEscape(filePath))
	if err != nil || resp == "" {
		return guidance.String()
	}

	// Parse to check if there are any symbols.
	var result struct {
		Nodes []any `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil || len(result.Nodes) <= 1 {
		return guidance.String()
	}

	fmt.Fprintf(&guidance, "\nFile context for %s:\n%s", filePath, resp)
	return guidance.String()
}

// enrichGrep provides symbol search results for the grep pattern and suggests graph alternatives.
func enrichGrep(toolInput map[string]any, port int) string {
	pattern, ok := toolInput["pattern"].(string)
	if !ok || len(pattern) < 3 {
		return ""
	}

	var guidance strings.Builder
	guidance.WriteString("[Gortex] PREFER graph tools over Grep:\n")
	guidance.WriteString("  - To find a symbol by name: use `search_symbols` (BM25 + camelCase-aware)\n")
	guidance.WriteString("  - To find all references: use `find_usages` (zero false positives)\n")
	guidance.WriteString("  - To find callers: use `get_callers`\n")
	guidance.WriteString("  - To find implementations: use `find_implementations`\n")

	resp, err := queryGortex(port, "/api/graph/search?q="+url.QueryEscape(pattern))
	if err != nil || resp == "" || resp == "[]" || resp == "[]\n" || resp == "null\n" {
		return guidance.String()
	}

	var nodes []any
	if err := json.Unmarshal([]byte(resp), &nodes); err != nil || len(nodes) == 0 {
		return guidance.String()
	}

	fmt.Fprintf(&guidance, "\n%d symbols match \"%s\" in the knowledge graph.", len(nodes), pattern)
	return guidance.String()
}

// enrichGlob suggests graph alternatives for file discovery.
func enrichGlob(toolInput map[string]any) string {
	pattern, ok := toolInput["pattern"].(string)
	if !ok || pattern == "" {
		return ""
	}

	// Only intervene for source file patterns.
	sourceExts := []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
		".kt", ".scala", ".swift", ".php", ".rb", ".ex", ".c", ".cpp",
		".cs", ".dart", ".lua", ".zig", ".ml", ".hs",
	}
	isSourceGlob := false
	lower := strings.ToLower(pattern)
	for _, ext := range sourceExts {
		if strings.HasSuffix(lower, ext) {
			isSourceGlob = true
			break
		}
	}
	if !isSourceGlob {
		return ""
	}

	return "[Gortex] PREFER graph tools over Glob for source files:\n" +
		"  - To find a symbol by name: use `search_symbols`\n" +
		"  - To find files containing a symbol: use `search_symbols` (returns file paths)\n" +
		"  - To understand file structure: use `get_file_summary`\n" +
		"  - For task-level file discovery: use `smart_context`"
}

func queryGortex(port int, path string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d%s", port, path))
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func looksLikeSourceFile(path string) bool {
	sourceExts := []string{
		".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
		".kt", ".scala", ".swift", ".php", ".rb", ".ex", ".exs",
		".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".cs",
		".sql", ".proto", ".sh", ".bash",
	}
	lower := strings.ToLower(path)
	for _, ext := range sourceExts {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}
