package main

import (
	"fmt"
	"io"
	"strings"

	gortexmcp "github.com/zzet/gortex/internal/mcp"
	"github.com/zzet/gortex/internal/toolref"
)

// maybeToolInvocationHint intercepts the `gortex <tool>` misuse: an agent that
// saw a bare MCP tool name and tried to run it as a top-level verb (e.g.
// `gortex read_file foo.go`). There is no such verb — the tool is reachable
// only as `gortex call <tool> --arg …`. Cobra would print a bare "unknown
// command" with no recovery path; instead, when the first positional argument
// names a registered MCP tool that is NOT already a cobra subcommand/alias,
// print a did-you-mean and return true so the caller exits nonzero.
//
// Cheap and daemon-free: the fast rejects (flag, known verb) run first, so the
// tool registry is only consulted for an argument that is otherwise an unknown
// command — never on a normal invocation.
func maybeToolInvocationHint(w io.Writer, args []string) bool {
	verb := firstPositionalArg(args)
	if verb == "" || strings.HasPrefix(verb, "-") {
		return false
	}
	if isKnownRootCommand(verb) {
		return false // a real cobra verb — let cobra route it
	}
	if gortexmcp.IsRegisteredToolName(verb) {
		fmt.Fprintf(w, "gortex: %q is not a gortex command, but it is a Gortex MCP tool.\n", verb)
		fmt.Fprintf(w, "Run it from a shell with:\n  %s\n", toolref.CLIFallback(verb))
		fmt.Fprintln(w, "General form: gortex call <tool> --arg k=v  (there is no bare `gortex <tool>` verb).")
		return true
	}

	// Not an exact tool name — try a conservative fuzzy match. Agents invent
	// truncations of the real tool names (`gortex index`, `gortex reindex`), so
	// a prefix / whole-token match against the registry converts those from a
	// dead end into recovery too. No match — let cobra's error stand.
	candidates := fuzzyToolCandidates(verb)
	if len(candidates) == 0 {
		return false
	}
	fmt.Fprintf(w, "gortex: unknown command %q. The closest Gortex MCP tools:\n", verb)
	for _, c := range candidates {
		fmt.Fprintf(w, "  %s\n", toolref.CLIFallback(c))
	}
	fmt.Fprintln(w, "General form: gortex call <tool> --arg k=v  (there is no bare `gortex <tool>` verb).")
	return true
}

// fuzzyMinVerbLen gates the fuzzy tool match: shorter fragments are too
// ambiguous to suggest anything for, so they fall through to cobra.
const fuzzyMinVerbLen = 4

// fuzzyToolCandidates returns up to two registered tool names the unknown verb
// plausibly meant, ranked prefix matches first (`reindex` → reindex_repository)
// then whole-token matches (`usages` → find_usages), alphabetical within a
// rank. Deliberately conservative — a prefix must land on an underscore
// boundary and a token must match exactly, so an unrelated verb never draws a
// suggestion and cobra's own error remains the fallback.
func fuzzyToolCandidates(verb string) []string {
	if len(verb) < fuzzyMinVerbLen {
		return nil
	}
	v := strings.ToLower(verb)
	var prefix, token []string
	for _, name := range gortexmcp.RegisteredToolNames() {
		switch {
		case strings.HasPrefix(name, v+"_"):
			prefix = append(prefix, name)
		case tokenOf(name, v):
			token = append(token, name)
		}
	}
	out := prefix
	out = append(out, token...)
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

// tokenOf reports whether v equals one of name's underscore-delimited tokens.
func tokenOf(name, v string) bool {
	for _, t := range strings.Split(name, "_") {
		if t == v {
			return true
		}
	}
	return false
}

// firstPositionalArg returns the first argument that is not an option flag,
// skipping the two value-taking persistent flags in their space-separated form
// so `gortex --config x read_file` still resolves to the intended verb. Stops
// at a "--" terminator.
func firstPositionalArg(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(a, "-") {
			if a == "--config" || a == "--log-level" {
				i++ // skip the flag's space-separated value
			}
			continue
		}
		return a
	}
	return ""
}

// isKnownRootCommand reports whether name matches a registered top-level cobra
// command or one of its aliases. No daemon, no tool registry — a plain walk of
// the already-registered command tree.
func isKnownRootCommand(name string) bool {
	for _, c := range rootCmd.Commands() {
		if c.Name() == name {
			return true
		}
		for _, a := range c.Aliases {
			if a == name {
				return true
			}
		}
	}
	return false
}
