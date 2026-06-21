package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	callIndex    string
	callJSON     string
	callJSONFile string
	callArgs     []string
	callFormat   string
	callDry      bool
	callQuiet    bool
)

// callDaemonTool is the daemon-tool relay seam. It is indirected through a
// package var so a test can stub the daemon call (and the catalog fetch used
// for name validation) without a running daemon.
var callDaemonTool = requireDaemonTool

var callCmd = &cobra.Command{
	Use:   "call <tool>",
	Short: "Invoke any registered MCP tool by name over the daemon",
	Long: `Invokes any tool the daemon's MCP surface registers — the generic escape
hatch when no dedicated CLI verb exists yet. The argument object is assembled
from three layers (last wins per key):

  1. a base object from --json-file <path> or --json - (stdin)
  2. an inline --json '<obj>' merged over the base
  3. one or more --arg key=value merged on top

--arg coercion is deterministic: true/false -> bool, an integer or float ->
number, null -> null, a value starting with [ or { -> parsed JSON, key:=<raw>
forces raw-JSON parse of the right-hand side (so version:="1.0" stays the
string "1.0"), key= -> the empty string, and anything else stays a string.
Repeating a key replaces the earlier value.

Use --dry to print the lowered argument object and the target tool without
calling the daemon (works with no daemon running). Use --format to pick the
wire format the tool renders (json|gcx|toon|text).

Requires a running daemon that tracks the repo (except for --dry).`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true, // a tool/daemon error should read cleanly, not dump usage
	RunE:         runCall,
}

func init() {
	callCmd.Flags().StringVar(&callIndex, "index", ".", "repository path the daemon must track")
	callCmd.Flags().StringVar(&callIndex, "repo", ".", "alias for --index")
	callCmd.Flags().StringVar(&callJSON, "json", "", "base argument object as inline JSON, or \"-\" to read from stdin")
	callCmd.Flags().StringVar(&callJSONFile, "json-file", "", "read the base argument object from a JSON file")
	callCmd.Flags().StringArrayVar(&callArgs, "arg", nil, "add one key=value argument (repeatable); see help for coercion rules")
	callCmd.Flags().StringVar(&callFormat, "format", "json", "output / wire format forwarded to the tool: json|gcx|toon|text")
	callCmd.Flags().BoolVar(&callDry, "dry", false, "print the lowered argument object and target tool without calling the daemon")
	callCmd.Flags().BoolVar(&callQuiet, "quiet", false, "suppress the stderr note when calling a mutating tool")
	rootCmd.AddCommand(callCmd)
}

func runCall(cmd *cobra.Command, args []string) error {
	tool := args[0]

	// Lower the argument object — pure-local, so --dry never needs a daemon.
	argObj, err := lowerCallArgs(cmd, callJSON, callJSONFile, callArgs)
	if err != nil {
		return err
	}

	if callDry {
		return printCallDry(cmd, tool, argObj)
	}

	// Best-effort name validation against the live daemon's catalog. When no
	// daemon is reachable this is skipped entirely so the call below returns
	// the normal daemonRequiredErr instead of a confusing "unknown tool".
	if cat, ok := fetchToolCatalog(callIndex); ok {
		if !cat.has(tool) {
			return unknownToolErr(tool, cat)
		}
		if cat.mutating(tool) && !callQuiet {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: %s writes to your working tree / graph\n", tool)
		}
	}

	// Forward the chosen wire format to the tool. The executor pins
	// format=json by default; an explicit format here overrides it.
	if callFormat != "" {
		argObj["format"] = callFormat
	}

	raw, err := callDaemonTool(callIndex, tool, argObj)
	if err != nil {
		return err
	}

	switch callFormat {
	case "gcx", "toon":
		// Compact wire formats are printed verbatim — re-indenting would
		// corrupt them.
		fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(string(raw), "\n"))
		return nil
	default: // json | text
		return emitDaemonJSON(cmd, raw)
	}
}

// printCallDry prints the lowered argument object (indented JSON) and the
// target tool name without touching the daemon.
func printCallDry(cmd *cobra.Command, tool string, argObj map[string]any) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "tool: %s\n", tool)
	fmt.Fprintln(out, "arguments:")
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(argObj)
}

// lowerCallArgs assembles the argument object from the three precedence layers:
// the --json-file / --json - base, an inline --json '<obj>' merged over it, and
// the --arg key=value pairs merged on top (last wins per key). It never touches
// the daemon, so the --dry path is fully exercisable offline.
func lowerCallArgs(cmd *cobra.Command, inlineJSON, jsonFile string, kvs []string) (map[string]any, error) {
	obj := map[string]any{}

	// Layer 1: base from --json-file <path> or --json - (stdin).
	if jsonFile != "" {
		data, err := os.ReadFile(jsonFile)
		if err != nil {
			return nil, fmt.Errorf("reading --json-file %s: %w", jsonFile, err)
		}
		if err := mergeJSONObject(obj, data, "--json-file"); err != nil {
			return nil, err
		}
	}
	if inlineJSON == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("reading --json from stdin: %w", err)
		}
		if err := mergeJSONObject(obj, data, "--json -"); err != nil {
			return nil, err
		}
	} else if inlineJSON != "" {
		// Layer 2: inline base merged over the file/stdin base.
		if err := mergeJSONObject(obj, []byte(inlineJSON), "--json"); err != nil {
			return nil, err
		}
	}

	// Layer 3: --arg key=value, merged on top (last occurrence of a key wins).
	for _, kv := range kvs {
		key, val, err := coerceArg(kv)
		if err != nil {
			return nil, err
		}
		obj[key] = val
	}
	return obj, nil
}

// mergeJSONObject decodes data as a JSON object and merges its keys into dst.
// A non-object payload (array, scalar) is rejected — tool arguments are always
// a key/value object.
func mergeJSONObject(dst map[string]any, data []byte, source string) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return fmt.Errorf("%s must be a JSON object: %w", source, err)
	}
	for k, v := range parsed {
		dst[k] = v
	}
	return nil
}

// coerceArg parses one --arg token into a (key, value) pair with deterministic
// type coercion. The grammar:
//
//	key:=<raw>  walrus — the right-hand side is parsed as raw JSON (so
//	            version:="1.0" stays the string "1.0", not a number)
//	key=value   value is coerced: true/false -> bool, int/float -> number,
//	            null -> null, a value starting with [ or { -> parsed JSON,
//	            key= -> empty string, everything else -> string
func coerceArg(token string) (string, any, error) {
	// Walrus first: a "key:=rhs" forces raw-JSON parse of the RHS. Detect the
	// ":=" boundary before the plain "=" so version:="1.0" is not mistaken for
	// a key of "version:" with an "=" value.
	if i := strings.Index(token, ":="); i >= 0 {
		key := token[:i]
		if key == "" {
			return "", nil, fmt.Errorf("--arg %q: empty key", token)
		}
		raw := token[i+2:]
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return "", nil, fmt.Errorf("--arg %q: right-hand side is not valid JSON: %w", token, err)
		}
		return key, v, nil
	}

	eq := strings.Index(token, "=")
	if eq < 0 {
		return "", nil, fmt.Errorf("--arg %q: expected key=value or key:=<json>", token)
	}
	key := token[:eq]
	if key == "" {
		return "", nil, fmt.Errorf("--arg %q: empty key", token)
	}
	val := token[eq+1:]
	return key, coerceScalar(val), nil
}

// coerceScalar applies the deterministic --arg value coercion to a bare value:
// true/false -> bool, integer/float -> number, null -> null, a value starting
// with [ or { -> parsed JSON (falling back to the literal string if it does not
// parse), "" -> empty string, everything else -> string.
func coerceScalar(val string) any {
	switch val {
	case "":
		return ""
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	// A leading [ or { signals an inline JSON array / object.
	if val[0] == '[' || val[0] == '{' {
		var v any
		if err := json.Unmarshal([]byte(val), &v); err == nil {
			return v
		}
		// Not valid JSON — keep the literal string so the value is not lost.
		return val
	}
	// Integers (json.Number-friendly: keep them as float64 like encoding/json
	// would, so the wire shape matches a parsed JSON object).
	if i, err := strconv.ParseInt(val, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return f
	}
	return val
}

// toolCatalog is the subset of the tool_profile response the call command uses
// for best-effort name validation and the mutating-tool note.
type toolCatalog struct {
	names    map[string]bool
	mutators map[string]bool
	all      []string
}

func (c *toolCatalog) has(name string) bool      { return c != nil && c.names[name] }
func (c *toolCatalog) mutating(name string) bool { return c != nil && c.mutators[name] }

// fetchToolCatalog asks the daemon for the full tool_profile and distills it
// into a toolCatalog. The bool result is false when no daemon is reachable (or
// the response is unusable) — the caller then SKIPS validation so the real call
// surfaces the normal daemonRequiredErr.
func fetchToolCatalog(repoPath string) (*toolCatalog, bool) {
	raw, err := callDaemonTool(repoPath, "tool_profile", map[string]any{})
	if err != nil {
		return nil, false
	}
	var profile struct {
		Live        []string `json:"live"`
		Deferred    []string `json:"deferred"`
		Descriptors []struct {
			Name     string `json:"name"`
			Mutating bool   `json:"mutating"`
		} `json:"descriptors"`
	}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return nil, false
	}
	cat := &toolCatalog{
		names:    map[string]bool{},
		mutators: map[string]bool{},
	}
	add := func(n string) {
		if n == "" || cat.names[n] {
			return
		}
		cat.names[n] = true
		cat.all = append(cat.all, n)
	}
	for _, n := range profile.Live {
		add(n)
	}
	for _, n := range profile.Deferred {
		add(n)
	}
	for _, d := range profile.Descriptors {
		add(d.Name)
		if d.Mutating {
			cat.mutators[d.Name] = true
		}
	}
	if len(cat.names) == 0 {
		return nil, false
	}
	sort.Strings(cat.all)
	return cat, true
}

// unknownToolErr builds the actionable error for an unknown tool name: it lists
// the nearest catalog matches (by edit distance, with a substring fallback) and
// points at `gortex tools search`.
func unknownToolErr(tool string, cat *toolCatalog) error {
	matches := cat.nearest(tool, 5)
	var b strings.Builder
	fmt.Fprintf(&b, "unknown tool %q", tool)
	if len(matches) > 0 {
		fmt.Fprintf(&b, " — did you mean: %s?", strings.Join(matches, ", "))
	}
	fmt.Fprintf(&b, "\nRun `gortex tools search %s` to find the right tool.", tool)
	return fmt.Errorf("%s", b.String())
}

// nearest returns up to n catalog names closest to query: every name that
// contains the query as a substring first (cheap, high-signal), then the
// remaining names ranked by Levenshtein distance.
func (c *toolCatalog) nearest(query string, n int) []string {
	if c == nil || len(c.all) == 0 {
		return nil
	}
	q := strings.ToLower(query)
	type scored struct {
		name string
		dist int
		sub  bool
	}
	ranked := make([]scored, 0, len(c.all))
	for _, name := range c.all {
		lower := strings.ToLower(name)
		ranked = append(ranked, scored{
			name: name,
			dist: levenshtein(q, lower),
			sub:  strings.Contains(lower, q) || strings.Contains(q, lower),
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].sub != ranked[j].sub {
			return ranked[i].sub // substring matches first
		}
		if ranked[i].dist != ranked[j].dist {
			return ranked[i].dist < ranked[j].dist
		}
		return ranked[i].name < ranked[j].name
	})
	out := make([]string, 0, n)
	for _, s := range ranked {
		// Keep substring matches always; otherwise require a reasonable
		// distance so we don't suggest wholly unrelated names.
		if s.sub || s.dist <= len(q)/2+2 {
			out = append(out, s.name)
		}
		if len(out) >= n {
			break
		}
	}
	return out
}

// levenshtein computes the edit distance between a and b with a rolling
// two-row buffer (O(len(a)*len(b)) time, O(len(b)) space).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
