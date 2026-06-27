package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	// javaValueKeyRe captures the property key of a `@Value("${key:default}")`
	// annotation — the part before any default. Group 1 is the property key.
	javaValueKeyRe = regexp.MustCompile(`@Value\s*\(\s*["']\$\{\s*([^:}\s]+)\s*(?::[^}]*)?\}`)
	// javaConfPropsRe captures an @ConfigurationProperties(prefix = "p") /
	// @ConfigurationProperties("p") prefix. Group 1 is the prefix.
	javaConfPropsRe = regexp.MustCompile(`@ConfigurationProperties\s*\(\s*(?:prefix\s*=\s*)?["']([^"']+)["']`)
	javaStringLitRe = regexp.MustCompile(`["']([^"']+)["']`)

	javaAnnotationPrefixArgRe = regexp.MustCompile(`(?:^|[,{ \t\r\n])prefix\s*=\s*(\{[^}]*\}|["'][^"']*["'])`)
	javaAnnotationNameArgRe   = regexp.MustCompile(`(?:^|[,{ \t\r\n])name\s*=\s*(\{[^}]*\}|["'][^"']*["'])`)
	javaAnnotationValueArgRe  = regexp.MustCompile(`(?:^|[,{ \t\r\n])value\s*=\s*(\{[^}]*\}|["'][^"']*["'])`)
)

// mineSpringConfigReads stamps the Spring property keys a bean reads onto its
// type node, so the resolver's BindSpringConfig pass can land reads_config edges
// to the application.yml/.properties config-key nodes. A field-level
// `@Value("${k}")` stamps the enclosing type; a class-level
// `@ConfigurationProperties(prefix)` stamps a `prefix.*` fanout marker on the
// type it annotates. Inert when neither annotation appears.
func mineSpringConfigReads(src []byte, result *parser.ExtractionResult) {
	hasValue := javaValueKeyRe.Match(src)
	hasProps := javaConfPropsRe.Match(src)
	if !hasValue && !hasProps {
		return
	}

	type typeRange struct {
		id    string
		start int
		end   int
	}
	var types []typeRange
	for _, n := range result.Nodes {
		if n.Meta != nil {
			if k, _ := n.Meta["kind"].(string); k == "annotation" {
				continue
			}
		}
		if n.Kind == graph.KindType || n.Kind == graph.KindInterface {
			types = append(types, typeRange{n.ID, n.StartLine, n.EndLine})
		}
	}
	if len(types) == 0 {
		return
	}
	nodeByID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		nodeByID[n.ID] = n
	}

	// enclosingType returns the type whose body contains line; nextType returns
	// the nearest type declared at or after line (an annotation sits above its
	// class).
	enclosingType := func(line int) string {
		best, bestStart := "", -1
		for _, t := range types {
			if line >= t.start && line <= t.end && t.start > bestStart {
				best, bestStart = t.id, t.start
			}
		}
		return best
	}
	nextType := func(line int) string {
		best, bestStart := "", int(^uint(0)>>1)
		for _, t := range types {
			if t.start >= line && t.start < bestStart {
				best, bestStart = t.id, t.start
			}
		}
		return best
	}
	stamp := func(typeID, key string) {
		if typeID == "" || key == "" {
			return
		}
		n := nodeByID[typeID]
		if n == nil {
			return
		}
		appendJavaSpringConfigKey(n, key)
	}

	if hasValue {
		for _, m := range javaValueKeyRe.FindAllSubmatchIndex(src, -1) {
			line := lineAt(src, m[0])
			key := string(src[m[2]:m[3]])
			id := enclosingType(line)
			if id == "" {
				id = nextType(line)
			}
			stamp(id, key)
		}
	}
	if hasProps {
		for _, m := range javaConfPropsRe.FindAllSubmatchIndex(src, -1) {
			line := lineAt(src, m[0])
			prefix := string(src[m[2]:m[3]])
			id := nextType(line)
			if id == "" {
				id = enclosingType(line)
			}
			stamp(id, prefix+".*")
		}
	}
}

func stampJavaSpringConfigAnnotations(n *graph.Node, anns []javaAnnotation) {
	for _, ann := range anns {
		name := ann.name
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if name != "ConditionalOnProperty" {
			if name != "ConfigurationProperties" {
				continue
			}
			if prefix := javaConfigurationPropertiesPrefix(ann.args); prefix != "" {
				appendJavaSpringConfigKey(n, prefix+".*")
			}
			continue
		}
		for _, key := range javaConditionalOnPropertyKeys(ann.args) {
			appendJavaSpringConfigKey(n, key)
		}
	}
}

func appendJavaSpringConfigKey(n *graph.Node, key string) {
	if n == nil || key == "" {
		return
	}
	if n.Meta == nil {
		n.Meta = map[string]any{}
	}
	existing, _ := n.Meta["spring_config_keys"].([]string)
	for _, e := range existing {
		if e == key {
			return
		}
	}
	n.Meta["spring_config_keys"] = append(existing, key)
}

func javaConditionalOnPropertyKeys(args string) []string {
	prefix := firstJavaAnnotationStringArg(args, "prefix")
	names := javaAnnotationStringArgs(args, "name")
	if len(names) == 0 {
		names = javaAnnotationStringArgs(args, "value")
	}
	if len(names) == 0 && !strings.Contains(args, "=") {
		names = javaStringLiterals(args)
	}

	var out []string
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := name
		if prefix != "" {
			key = strings.TrimSuffix(prefix, ".") + "." + strings.TrimPrefix(name, ".")
		}
		if key != "" && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func javaConfigurationPropertiesPrefix(args string) string {
	if prefix := firstJavaAnnotationStringArg(args, "prefix"); prefix != "" {
		return prefix
	}
	if !strings.Contains(args, "=") {
		return firstJavaStringLiteral(args)
	}
	return ""
}

func firstJavaAnnotationStringArg(args, name string) string {
	vals := javaAnnotationStringArgs(args, name)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func javaAnnotationStringArgs(args, name string) []string {
	re := javaAnnotationStringArgRe(name)
	if re == nil {
		return nil
	}
	m := re.FindStringSubmatch(args)
	if len(m) < 2 {
		return nil
	}
	return javaStringLiterals(m[1])
}

func javaAnnotationStringArgRe(name string) *regexp.Regexp {
	switch name {
	case "prefix":
		return javaAnnotationPrefixArgRe
	case "name":
		return javaAnnotationNameArgRe
	case "value":
		return javaAnnotationValueArgRe
	default:
		return nil
	}
}

func javaStringLiterals(s string) []string {
	matches := javaStringLitRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

func firstJavaStringLiteral(s string) string {
	vals := javaStringLiterals(s)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}
