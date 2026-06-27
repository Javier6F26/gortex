package contracts

import (
	"strings"

	"github.com/zzet/gortex/internal/graph"
)

// Spring configuration-key graph. application(-profile)?.{yml,yaml,properties}
// leaf keys become KindConfigKey nodes (values REDACTED — key-only, so a secret
// in application.yml never enters the graph), and @Value("${k:default}") /
// @ConfigurationProperties(prefix) reads become EdgeReadsConfig edges to them.
// Both sides canonicalize the key the way Spring's relaxed binding does, so a
// kebab-case property file key binds to a camelCase @Value reference — letting
// "which beans read this property" resolve in one traversal over the existing
// reads_config capability surface.

// canonicalizeSpringKey applies Spring relaxed binding: per dot-separated
// segment, lowercase and drop '-' and '_'. So my-prop / myProp / MY_PROP and
// my.prop's leaf all canonicalize to the same form, letting a property-file key
// and a code reference bind regardless of which relaxed spelling each used.
func canonicalizeSpringKey(key string) string {
	segs := strings.Split(key, ".")
	for i, s := range segs {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "-", "")
		s = strings.ReplaceAll(s, "_", "")
		segs[i] = s
	}
	return strings.Join(segs, ".")
}

// springConfigKeyID is the canonical KindConfigKey node ID for a Spring property.
func springConfigKeyID(key string) string {
	return "cfg::spring::" + canonicalizeSpringKey(key)
}

// springConfigFile reports whether path is a Spring application config file and
// returns its profile (the `-prod` in application-prod.yml; "" for the base).
func springConfigFile(path string) (profile string, ok bool) {
	path = strings.ReplaceAll(path, "\\", "/")
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	for _, ext := range []string{".yml", ".yaml", ".properties"} {
		if !strings.HasSuffix(base, ext) {
			continue
		}
		stem := strings.TrimSuffix(base, ext)
		switch {
		case stem == "application":
			return "", true
		case strings.HasPrefix(stem, "application-"):
			return stem[len("application-"):], true
		}
	}
	return "", false
}

// ExtractSpringConfigKeys parses every leaf key from a Spring application config
// file into a value-redacted KindConfigKey node. The node's Name is the raw
// (developer-written) key for readability; the canonical form is the ID.
func ExtractSpringConfigKeys(filePath string, src []byte, profile string) []*graph.Node {
	var keys []string
	if strings.HasSuffix(filePath, ".properties") {
		keys = parsePropertiesKeys(src)
	} else {
		keys = parseYAMLLeafKeys(src)
	}
	var out []*graph.Node
	seen := map[string]bool{}
	for _, k := range keys {
		id := springConfigKeyID(k)
		if k == "" || seen[id] {
			continue
		}
		seen[id] = true
		meta := map[string]any{
			"source":         "spring",
			"raw_key":        k,
			"value_redacted": true,
		}
		if profile != "" {
			meta["profile"] = profile
		}
		out = append(out, &graph.Node{
			ID: id, Kind: graph.KindConfigKey, Name: k,
			FilePath: filePath, StartLine: 1, Meta: meta,
		})
	}
	return out
}

// BindSpringConfig emits the Spring config-key nodes from every application
// config file in the graph (read via srcFor) and a reads_config edge from each
// bean that carries spring_config_keys Meta (stamped by the Java extractor on a
// @Value / @ConfigurationProperties bean) to the key it reads — with relaxed
// canonicalization so spelling differences still bind, and a `*` suffix
// (@ConfigurationProperties prefix) fanning out to every key under the prefix.
// Returns the number of nodes + edges added.
func BindSpringConfig(g graph.Store, srcFor func(string) []byte) int {
	if g == nil || srcFor == nil {
		return 0
	}

	added := 0
	// Canonical prefix -> the key IDs declared under it, for prefix-read fanout.
	byPrefix := map[string][]string{}
	for fn := range g.NodesByKind(graph.KindFile) {
		if fn == nil {
			continue
		}
		profile, ok := springConfigFile(fn.FilePath)
		if !ok {
			continue
		}
		src := srcFor(fn.FilePath)
		if src == nil {
			continue
		}
		for _, kn := range ExtractSpringConfigKeys(fn.FilePath, src, profile) {
			if g.GetNode(kn.ID) == nil {
				g.AddNode(kn)
				added++
			}
			canon := strings.TrimPrefix(kn.ID, "cfg::spring::")
			if dot := strings.LastIndexByte(canon, '.'); dot >= 0 {
				prefix := canon[:dot]
				byPrefix[prefix] = append(byPrefix[prefix], kn.ID)
			}
		}
	}

	edges := 0
	seenEdge := map[string]bool{}
	emit := func(from, keyID, raw string) {
		k := from + "\x00" + keyID
		if seenEdge[k] {
			return
		}
		seenEdge[k] = true
		fromNode := g.GetNode(from)
		fp, line := "", 0
		if fromNode != nil {
			fp, line = fromNode.FilePath, fromNode.StartLine
		}
		g.AddEdge(&graph.Edge{
			From: from, To: keyID, Kind: graph.EdgeReadsConfig,
			FilePath: fp, Line: line,
			Meta: map[string]any{"via": "spring_value", "raw_key": raw},
		})
		edges++
	}

	for _, n := range springReaderNodes(g) {
		for _, key := range springConfigKeysOf(n) {
			if strings.HasSuffix(key, "*") {
				// @ConfigurationProperties(prefix) — bind to every key under it.
				prefix := canonicalizeSpringKey(strings.TrimSuffix(strings.TrimSuffix(key, "*"), "."))
				for _, keyID := range byPrefix[prefix] {
					emit(n.ID, keyID, key)
				}
				continue
			}
			emit(n.ID, springConfigKeyID(key), key)
		}
	}
	return added + edges
}

// springReaderNodes returns every node that carries a spring_config_keys hint.
func springReaderNodes(g graph.Store) []*graph.Node {
	var out []*graph.Node
	for _, kind := range []graph.NodeKind{graph.KindField, graph.KindType, graph.KindInterface, graph.KindMethod} {
		for n := range g.NodesByKind(kind) {
			if n != nil && n.Meta != nil {
				if _, ok := n.Meta["spring_config_keys"]; ok {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// springConfigKeysOf coerces the spring_config_keys Meta value into a slice.
func springConfigKeysOf(n *graph.Node) []string {
	switch t := n.Meta["spring_config_keys"].(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// parsePropertiesKeys returns the keys of a .properties file (left of the first
// '=' or ':' on each non-comment line).
func parsePropertiesKeys(src []byte) []string {
	var keys []string
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		sep := strings.IndexAny(line, "=:")
		if sep <= 0 {
			continue
		}
		if key := strings.TrimSpace(line[:sep]); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// parseYAMLLeafKeys returns the dotted path of every leaf (value-bearing) key in
// a YAML document, tracking nesting by indentation. Sequence items and block
// scalars are skipped — only the mapping keys that name a property are emitted.
func parseYAMLLeafKeys(src []byte) []string {
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	var keys []string
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		ci := strings.IndexByte(trimmed, ':')
		if ci < 0 {
			continue
		}
		indent := len(line) - len(trimmed)
		key := strings.TrimSpace(trimmed[:ci])
		val := strings.TrimSpace(trimmed[ci+1:])
		// Pop frames at or deeper than this indent — we've left their scope.
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parts := make([]string, 0, len(stack)+1)
		for _, f := range stack {
			parts = append(parts, f.key)
		}
		parts = append(parts, key)
		dotted := strings.Join(parts, ".")
		if val != "" && val != "|" && val != ">" && val != "|-" && val != ">-" {
			keys = append(keys, dotted)
		} else {
			stack = append(stack, frame{indent: indent, key: key})
		}
	}
	return keys
}
