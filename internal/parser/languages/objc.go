package languages

import (
	"regexp"
	"strings"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// Objective-C / Objective-C++ is C-derived with `@` directives for
// class, protocol, and implementation declarations. Methods use a
// keyword-argument selector syntax: `- (ret)fooWith:(T)a andBar:(T)b`.
var (
	objcInterfaceRe = regexp.MustCompile(`(?m)^\s*@interface\s+(\w+)`)
	// objcSuperRe captures the superclass of `@interface X : Super` (group 1).
	objcSuperRe    = regexp.MustCompile(`@interface\s+\w+\s*:\s*(\w+)`)
	objcProtocolRe = regexp.MustCompile(`(?m)^\s*@protocol\s+(\w+)`)
	objcImplRe     = regexp.MustCompile(`(?m)^\s*@implementation\s+(\w+)`)
	objcMethodRe   = regexp.MustCompile(`(?m)^\s*([-+])\s*\(\s*[^)]*\)\s*([A-Za-z_]\w*(?:\s*:\s*\([^)]*\)\s*\w+(?:\s+[A-Za-z_]\w*\s*:\s*\([^)]*\)\s*\w+)*)?)`)
	objcFuncRe     = regexp.MustCompile(`(?m)^\s*(?:static\s+|extern\s+|inline\s+)*[A-Za-z_][\w\s\*]*?\s+([A-Za-z_]\w*)\s*\([^)]*\)\s*\{`)
	objcImportQRe  = regexp.MustCompile(`(?m)^\s*#import\s+"([^"]+)"`)
	objcImportARe  = regexp.MustCompile(`(?m)^\s*#import\s+<([^>]+)>`)
	objcAtImportRe = regexp.MustCompile(`(?m)^\s*@import\s+([\w.]+)`)
)

// ObjCExtractor extracts Objective-C / Objective-C++ source using regex.
type ObjCExtractor struct{}

func NewObjCExtractor() *ObjCExtractor { return &ObjCExtractor{} }

// objcSuperclass returns the superclass named on the `@interface` line that
// contains the byte at pos (the class name), or "" for a class with no
// superclass (a category declaration). pos is the name offset rather than the
// match start because the interface regex's leading `\s*` can span a preceding
// blank line.
func objcSuperclass(src []byte, pos int) string {
	start := pos
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := pos
	for end < len(src) && src[end] != '\n' {
		end++
	}
	if m := objcSuperRe.FindSubmatch(src[start:end]); m != nil {
		return string(m[1])
	}
	return ""
}

// stampObjCNodeMeta sets a Meta key on the already-emitted node with the given
// ID.
func stampObjCNodeMeta(result *parser.ExtractionResult, id, key string, value any) {
	for _, n := range result.Nodes {
		if n.ID == id {
			if n.Meta == nil {
				n.Meta = map[string]any{}
			}
			n.Meta[key] = value
			return
		}
	}
}

func (e *ObjCExtractor) Language() string     { return "objc" }
func (e *ObjCExtractor) Extensions() []string { return []string{".m", ".mm"} }

func (e *ObjCExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	lines := strings.Split(string(src), "\n")
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID: filePath, Kind: graph.KindFile, Name: filePath,
		FilePath: filePath, StartLine: 1, EndLine: len(lines),
		Language: "objc",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	add := func(name string, kind graph.NodeKind, start, end int) {
		if name == "" {
			return
		}
		id := filePath + "::" + name
		if seen[id] {
			return
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: kind, Name: name,
			FilePath: filePath, StartLine: start, EndLine: end,
			Language: "objc",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: start,
		})
	}

	for _, m := range objcInterfaceRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, findKeywordBlockEnd(lines, line, "@end"))
		// UIKit classification: `@interface X : UIViewController` → stamp the
		// uikit_role its superclass implies (shared with the Swift extractor).
		if super := objcSuperclass(src, m[2]); super != "" {
			if role := uikitRoleFor(map[string]bool{super: true}); role != "" {
				stampObjCNodeMeta(result, filePath+"::"+name, "uikit_role", role)
			}
		}
		// Protocol conformance: `@interface X : Base <P1, P2>` adopts P1, P2;
		// stamp them for the Swift<->ObjC bridge synthesizer.
		if protos := objcConformedProtocols(src, m[0]); protos != "" {
			stampObjCNodeMeta(result, filePath+"::"+name, "objc_protocols", protos)
		}
	}
	for _, m := range objcProtocolRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindInterface, line, findKeywordBlockEnd(lines, line, "@end"))
	}
	for _, m := range objcImplRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, findKeywordBlockEnd(lines, line, "@end"))
	}

	// Methods: build a selector name and capture the +/- class-vs-instance
	// marker and the return type.
	for _, m := range objcMethodRe.FindAllSubmatchIndex(src, -1) {
		if m[4] < 0 {
			continue
		}
		sel := objcBuildSelector(string(src[m[4]:m[5]]))
		if sel == "" {
			continue
		}
		id := filePath + "::" + sel
		if seen[id] {
			continue
		}
		seen[id] = true
		line := lineAt(src, m[0])
		meta := map[string]any{}
		if string(src[m[2]:m[3]]) == "+" {
			meta["is_static"] = true // class method
		}
		if rt := objcReturnType(src[m[0]:m[4]]); rt != "" {
			meta["return_type"] = rt
		}
		node := &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: sel,
			FilePath: filePath, StartLine: line, EndLine: findBlockEnd(lines, line),
			Language: "objc",
		}
		if len(meta) > 0 {
			node.Meta = meta
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: line,
		})
	}

	// C-style function definitions.
	for _, m := range objcFuncRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		if objcIsKeyword(name) {
			continue
		}
		line := lineAt(src, m[0])
		add(name, graph.KindFunction, line, findBlockEnd(lines, line))
	}

	// React Native native-module exports: RCT_EXPORT_MODULE declares the
	// JS-visible module, RCT_EXPORT_METHOD / RCT_REMAP_METHOD expose a
	// method to JS. Emit a method node per export carrying rn_module /
	// rn_method so the React-Native bridge synthesizer can land a JS
	// `NativeModules.<module>.<method>()` call on this native impl.
	for _, rx := range extractObjCRNExports(src) {
		id := filePath + "::" + rx.selector
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindMethod, Name: rx.selector,
			FilePath: filePath, StartLine: rx.line, EndLine: findBlockEnd(lines, rx.line),
			Language: "objc",
			Meta:     map[string]any{"rn_module": rx.module, "rn_method": rx.jsName},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines,
			FilePath: filePath, Line: rx.line,
		})
	}

	// React Native Fabric / Paper view managers: an @implementation that
	// exports view properties backs a JS component. Emit a component node
	// so the Fabric synthesizer can link it to the codegen TS spec.
	for _, fm := range extractObjCFabricManagers(src) {
		id := filePath + "::fabric:" + fm.component
		if seen[id] {
			continue
		}
		seen[id] = true
		node := &graph.Node{
			ID: id, Kind: graph.KindType, Name: fm.component,
			FilePath: filePath, StartLine: fm.line, EndLine: findBlockEnd(lines, fm.line),
			Language: "objc",
			Meta:     map[string]any{"fabric_component": fm.component, "fabric_native": "objc"},
		}
		if len(fm.props) > 0 {
			node.Meta["fabric_props"] = fm.props
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: fm.line,
		})
	}

	emitImport := func(mod string, line int, kind string) {
		if mod == "" {
			return
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + mod,
			Kind: graph.EdgeImports, FilePath: filePath, Line: line,
			Meta: map[string]any{"include_kind": kind},
		})
	}
	// `#import "Foo.h"` is a local (quoted) include; `#import <X>` / `@import X`
	// are system / module imports.
	for _, m := range objcImportQRe.FindAllSubmatchIndex(src, -1) {
		emitImport(string(src[m[2]:m[3]]), lineAt(src, m[0]), "quoted")
	}
	for _, m := range objcImportARe.FindAllSubmatchIndex(src, -1) {
		emitImport(string(src[m[2]:m[3]]), lineAt(src, m[0]), "system")
	}
	for _, m := range objcAtImportRe.FindAllSubmatchIndex(src, -1) {
		emitImport(string(src[m[2]:m[3]]), lineAt(src, m[0]), "module")
	}

	// Message-send call edges: attribute each [recv selector:...] to the method
	// whose body it appears in. This is the no-LSP call graph for Objective-C.
	methodRanges := objcMethodRanges(src, lines, filePath)
	for _, ms := range objcMessageSends(src) {
		from := objcEnclosing(methodRanges, ms.line)
		if from == "" {
			continue
		}
		result.Edges = append(result.Edges, &graph.Edge{
			From: from, To: "unresolved::" + ms.selector,
			Kind: graph.EdgeReferences, FilePath: filePath, Line: ms.line,
		})
	}

	// React Native native event emits pair with the JS addListener handler.
	mineRNNativeEmits(src, rnObjCSendEventRe, func(line int) string {
		return objcEnclosing(methodRanges, line)
	}, filePath, "objc", result)

	// A custom paren-form sendEvent(...) wrapper (distinct from the bracketed
	// sendEventWithName:) is also an RN emit; the two forms are syntactically
	// disjoint, so mining it separately does not double-count.
	mineRNNativeEmits(src, rnSendEventWrapperRe, func(line int) string {
		return objcEnclosing(methodRanges, line)
	}, filePath, "objc", result)

	// @selector(doThing:) literals reference a method by name without a call
	// edge; capture each as a function-as-value reference to the selector.
	captureObjCSelectors(src, methodRanges, filePath, result)

	// @property declarations become field members of their enclosing class,
	// carrying the declared type and the owning class so the property is a
	// fully-attributed field, not a bare name.
	classRanges := objcClassRanges(src, lines, filePath)
	for _, m := range objcPropertyRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		propID := filePath + "::" + name + "#prop"
		if seen[propID] {
			continue
		}
		seen[propID] = true
		owner := objcEnclosing(classRanges, line)
		meta := map[string]any{}
		if owner != "" {
			meta["receiver"] = owner
		}
		if ft := objcPropertyType(string(src[m[0]:m[2]])); ft != "" {
			meta["field_type"] = ft
		}
		node := &graph.Node{
			ID: propID, Kind: graph.KindField, Name: name,
			FilePath: filePath, StartLine: line, EndLine: line, Language: "objc",
		}
		if len(meta) > 0 {
			node.Meta = meta
		}
		result.Nodes = append(result.Nodes, node)
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: propID, Kind: graph.EdgeDefines, FilePath: filePath, Line: line,
		})
		if owner != "" {
			result.Edges = append(result.Edges, &graph.Edge{
				From: propID, To: owner, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: line,
			})
		}
	}

	// typedef NS_ENUM / NS_OPTIONS become named type nodes.
	for _, m := range objcTypedefRe.FindAllSubmatchIndex(src, -1) {
		name := string(src[m[2]:m[3]])
		line := lineAt(src, m[0])
		add(name, graph.KindType, line, line)
	}

	return result, nil
}

// objcBuildSelector takes the captured argument-slice of a method
// signature and returns the canonical selector with trailing colons
// for each keyword part, e.g. `fooWith:andBar:`.
// objcPropertyType extracts the declared type from an @property line prefix
// (the text up to the property name): strips `@property`, the attribute list,
// and the trailing pointer/whitespace, leaving e.g. "NSString" from
// `@property (nonatomic) NSString *`.
func objcPropertyType(prefix string) string {
	i := strings.Index(prefix, "@property")
	if i < 0 {
		return ""
	}
	s := strings.TrimSpace(prefix[i+len("@property"):])
	if strings.HasPrefix(s, "(") {
		if j := strings.IndexByte(s, ')'); j >= 0 {
			s = s[j+1:]
		}
	}
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, " \t*")
	return strings.TrimSpace(s)
}

func objcBuildSelector(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// No colons — unary selector like `viewDidLoad`.
	if !strings.Contains(raw, ":") {
		// Take the first identifier.
		for i, r := range raw {
			isIdent := r == '_' ||
				(r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9' && i > 0)
			if !isIdent {
				return raw[:i]
			}
		}
		return raw
	}
	var parts []string
	depth := 0
	cur := strings.Builder{}
	for _, r := range raw {
		switch r {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth > 0 {
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' {
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	var sel strings.Builder
	for _, p := range parts {
		idx := strings.Index(p, ":")
		if idx < 0 {
			continue
		}
		sel.WriteString(p[:idx])
		sel.WriteByte(':')
	}
	return sel.String()
}

func objcIsKeyword(s string) bool {
	switch s {
	case "if", "else", "while", "for", "do", "switch", "case", "default",
		"return", "break", "continue", "sizeof", "typedef", "struct",
		"enum", "union", "static", "extern", "inline", "const", "void":
		return true
	}
	return false
}

// objcRNExport is one React Native method export discovered in a file:
// its full ObjC selector, the JS-visible method name, the JS module name
// (the enclosing @implementation's RCT_EXPORT_MODULE name, defaulting to
// the class name), and the source line.
type objcRNExport struct {
	selector string
	jsName   string
	module   string
	line     int
}

var (
	objcRCTModuleRe = regexp.MustCompile(`RCT_EXPORT_MODULE(?:_NO_LOAD)?\s*\(\s*([A-Za-z_]\w*)?\s*\)`)
	objcRCTMethodRe = regexp.MustCompile(`RCT_(EXPORT|REMAP)_METHOD\s*\(`)
)

// extractObjCRNExports scans for React Native module/method export
// macros and resolves each exported method to its JS module + method
// name. Module attribution walks the enclosing @implementation block:
// RCT_EXPORT_MODULE(arg) sets the JS name (arg, or the class name when
// the macro has no argument).
func extractObjCRNExports(src []byte) []objcRNExport {
	blocks := objcImplBlocks(src)
	blockFor := func(off int) *objcImplBlock { return objcBlockForOffset(blocks, off) }

	// RCT_EXPORT_MODULE: set the JS module name for its block.
	for _, m := range objcRCTModuleRe.FindAllSubmatchIndex(src, -1) {
		b := blockFor(m[0])
		if b == nil {
			continue
		}
		if m[2] >= 0 {
			if arg := strings.TrimSpace(string(src[m[2]:m[3]])); arg != "" {
				b.moduleName = arg
			}
		}
	}

	var out []objcRNExport
	for _, loc := range objcRCTMethodRe.FindAllSubmatchIndex(src, -1) {
		kind := string(src[loc[2]:loc[3]]) // EXPORT | REMAP
		openParen := loc[1] - 1            // the '(' the regex ended on
		inner, ok := objcBalancedParen(src, openParen)
		if !ok {
			continue
		}
		module := ""
		if b := blockFor(loc[0]); b != nil {
			module = b.moduleName
		}
		var selector, jsName string
		if kind == "REMAP" {
			// RCT_REMAP_METHOD(jsName, selectorParts...)
			js, rest := objcSplitFirstComma(inner)
			jsName = strings.TrimSpace(js)
			selector = objcBuildSelector(rest)
		} else {
			selector = objcBuildSelector(inner)
			jsName = selector
			if i := strings.IndexByte(jsName, ':'); i >= 0 {
				jsName = jsName[:i]
			}
		}
		if selector == "" || jsName == "" || module == "" {
			continue
		}
		out = append(out, objcRNExport{
			selector: selector,
			jsName:   jsName,
			module:   module,
			line:     lineAt(src, loc[0]),
		})
	}
	return out
}

// objcBalancedParen returns the text between the '(' at openIdx and its
// matching ')', honouring nested parentheses (ObjC type casts like
// `(NSString *)`). ok is false when no balanced close is found.
func objcBalancedParen(src []byte, openIdx int) (inner string, ok bool) {
	if openIdx < 0 || openIdx >= len(src) || src[openIdx] != '(' {
		return "", false
	}
	depth := 0
	for i := openIdx; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return string(src[openIdx+1 : i]), true
			}
		}
	}
	return "", false
}

// objcSplitFirstComma splits s at the first top-level comma (depth-0),
// used to peel the JS name off an RCT_REMAP_METHOD's first argument.
func objcSplitFirstComma(s string) (first, rest string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return s[:i], s[i+1:]
			}
		}
	}
	return s, ""
}

// objcKeywordEndOffset returns the byte offset just past the first line
// containing keyword at or after `from`, or -1 if none. Used to bound an
// @implementation block at its @end.
func objcKeywordEndOffset(src []byte, from int, keyword string) int {
	idx := strings.Index(string(src[from:]), keyword)
	if idx < 0 {
		return -1
	}
	return from + idx + len(keyword)
}

// objcImplBlock is one @implementation block: its class name, byte range
// (start of the @implementation directive to its @end), and the JS module
// name (defaults to the class name; RCT_EXPORT_MODULE may override it).
type objcImplBlock struct {
	name       string
	start, end int
	moduleName string
}

// objcImplBlocks returns every @implementation block in src with its byte
// range, used to attribute macros (RCT_EXPORT_*) to their enclosing class.
func objcImplBlocks(src []byte) []objcImplBlock {
	var blocks []objcImplBlock
	implIdx := objcImplRe.FindAllSubmatchIndex(src, -1)
	for i, m := range implIdx {
		start := m[0]
		end := len(src)
		if i+1 < len(implIdx) {
			end = implIdx[i+1][0]
		}
		if e := objcKeywordEndOffset(src, m[1], "@end"); e >= 0 && e < end {
			end = e
		}
		name := string(src[m[2]:m[3]])
		blocks = append(blocks, objcImplBlock{name: name, start: start, end: end, moduleName: name})
	}
	return blocks
}

// objcBlockForOffset returns the @implementation block containing off.
func objcBlockForOffset(blocks []objcImplBlock, off int) *objcImplBlock {
	for i := range blocks {
		if off >= blocks[i].start && off < blocks[i].end {
			return &blocks[i]
		}
	}
	return nil
}

// objcRCTViewPropRe matches RCT_EXPORT_VIEW_PROPERTY(propName, type) and
// RCT_REMAP_VIEW_PROPERTY(propName, ...) — the markers of a Fabric / Paper
// view manager.
var objcRCTViewPropRe = regexp.MustCompile(`RCT_(?:EXPORT|REMAP)_VIEW_PROPERTY\s*\(\s*([A-Za-z_]\w*)`)

// objcFabricManager is a native view manager discovered in a file: the JS
// component name it backs, the props it exports, and the source line.
type objcFabricManager struct {
	component string
	props     []string
	line      int
}

// extractObjCFabricManagers finds @implementation blocks that export view
// properties (RCT_EXPORT_VIEW_PROPERTY) — i.e. RN view managers — and
// resolves the JS component name from the class name (RN strips a trailing
// "Manager"). One entry per such block, with the exported prop names.
func extractObjCFabricManagers(src []byte) []objcFabricManager {
	propLocs := objcRCTViewPropRe.FindAllSubmatchIndex(src, -1)
	if len(propLocs) == 0 {
		return nil
	}
	blocks := objcImplBlocks(src)
	propsByBlock := map[int][]string{}
	for _, loc := range propLocs {
		b := objcBlockForOffset(blocks, loc[0])
		if b == nil {
			continue
		}
		propsByBlock[b.start] = append(propsByBlock[b.start], string(src[loc[2]:loc[3]]))
	}
	var out []objcFabricManager
	for i := range blocks {
		props, ok := propsByBlock[blocks[i].start]
		if !ok {
			continue
		}
		out = append(out, objcFabricManager{
			component: objcComponentName(blocks[i].name),
			props:     props,
			line:      lineAt(src, blocks[i].start),
		})
	}
	return out
}

// objcComponentName maps a view-manager class name to its JS component
// name by stripping the RN "Manager" suffix. RN names a view manager
// "<ComponentName>Manager" — and the component name itself usually ends
// in "View" (RCTColorView → RCTColorViewManager), so only the final
// "Manager" is removed.
func objcComponentName(class string) string {
	const sfx = "Manager"
	if len(class) > len(sfx) && strings.HasSuffix(class, sfx) {
		return class[:len(class)-len(sfx)]
	}
	return class
}

var _ parser.Extractor = (*ObjCExtractor)(nil)

// objcConformedProtocols returns the comma-joined protocol names adopted on
// an @interface declaration line (`@interface X : Base <P1, P2>` -> "P1,P2"),
// or "" when none are adopted. The `<...>` clause on the declaration line is
// the adopted-protocol list.
func objcConformedProtocols(src []byte, from int) string {
	end := from
	for end < len(src) && src[end] != '\n' {
		end++
	}
	line := string(src[from:end])
	open := strings.IndexByte(line, '<')
	if open < 0 {
		return ""
	}
	closeIdx := strings.IndexByte(line[open:], '>')
	if closeIdx < 0 {
		return ""
	}
	var protos []string
	for _, pr := range strings.Split(line[open+1:open+closeIdx], ",") {
		if pr = strings.TrimSpace(pr); pr != "" {
			protos = append(protos, pr)
		}
	}
	return strings.Join(protos, ",")
}
