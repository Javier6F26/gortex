package languages

import (
	"regexp"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

var (
	// ccDispatchRe matches a closure-collection dispatcher: a method iterating
	// a collection property and invoking each element — `prop.forEach { $0() }`
	// / `{ it() }`. The element-invoke is the gate that proves the collection
	// holds closures. Group 1 is the collection field name.
	ccDispatchRe = regexp.MustCompile(`(\w+)\.forEach\s*\{\s*(?:\$0|it)\s*\(`)
	// ccAppendRe matches a closure-collection registrar: an append onto a
	// collection property. Broad by design — only fields that also have a
	// dispatcher (above) get paired, so unrelated `.append` is harmless.
	ccAppendRe = regexp.MustCompile(`(\w+)\.(?:append|add|push|insert)\s*\(`)
)

// mineSwiftClosureCollections stamps each enclosing method with the
// closure-collection field it dispatches (cc_dispatch_field) or appends to
// (cc_append_field), so the resolver can pair dispatchers with registrars by
// field name across files and classes.
func mineSwiftClosureCollections(src []byte, funcRanges []funcRange, result *parser.ExtractionResult) {
	if !ccDispatchRe.Match(src) && !ccAppendRe.Match(src) {
		return
	}
	nodeByID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindMethod || n.Kind == graph.KindFunction {
			nodeByID[n.ID] = n
		}
	}
	stamp := func(line int, key, field string) {
		if field == "" {
			return
		}
		n := nodeByID[findEnclosingFunc(funcRanges, line)]
		if n == nil {
			return
		}
		if n.Meta == nil {
			n.Meta = map[string]any{}
		}
		if _, ok := n.Meta[key]; !ok {
			n.Meta[key] = field
		}
	}
	for _, m := range ccDispatchRe.FindAllSubmatchIndex(src, -1) {
		stamp(lineAt(src, m[0]), "cc_dispatch_field", string(src[m[2]:m[3]]))
	}
	for _, m := range ccAppendRe.FindAllSubmatchIndex(src, -1) {
		stamp(lineAt(src, m[0]), "cc_append_field", string(src[m[2]:m[3]]))
	}
}
