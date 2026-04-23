package languages

import (
	"strings"

	sitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

const (
	qRbClass = `(class name: (constant) @class.name) @class.def`

	qRbModule = `(module name: (constant) @mod.name) @mod.def`

	qRbMethod = `(method name: (identifier) @method.name) @method.def`

	qRbCall = `(call method: (identifier) @call.name) @call.expr`

	qRbRequire = `(call
		method: (identifier) @req.method
		arguments: (argument_list
			(string (string_content) @req.path))) @req.def`

	qRbClassMethod = `(class
		name: (constant) @class.name
		body: (body_statement
			(method
				name: (identifier) @method.name) @method.def))`

	// `def self.foo` appears in the grammar as singleton_method (not
	// method). Ruby class methods live in this branch exclusively; a
	// Rails User.authenticate / Rails.logger style factory is one of
	// these, so the extractor would miss them without a second query.
	qRbSingletonMethod = `(class
		name: (constant) @class.name
		body: (body_statement
			(singleton_method
				name: (identifier) @method.name) @method.def))`

	qRbAssignment = `(assignment
		left: (constant) @const.name
		right: (_) @const.value) @const.def`
)

// RubyExtractor extracts Ruby source files into graph nodes and edges.
type RubyExtractor struct {
	lang *sitter.Language
}

func NewRubyExtractor() *RubyExtractor {
	return &RubyExtractor{lang: grammars.RubyLanguage()}
}

func (e *RubyExtractor) Language() string     { return "ruby" }
func (e *RubyExtractor) Extensions() []string { return []string{".rb", ".rake", ".gemspec"} }

func (e *RubyExtractor) Extract(filePath string, src []byte) (*parser.ExtractionResult, error) {
	tree, err := parser.ParseFile(src, e.lang)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	result := &parser.ExtractionResult{}

	fileNode := &graph.Node{
		ID:        filePath,
		Kind:      graph.KindFile,
		Name:      filePath,
		FilePath:  filePath,
		StartLine: 1,
		EndLine:   int(root.EndPoint().Row) + 1,
		Language:  "ruby",
	}
	result.Nodes = append(result.Nodes, fileNode)

	seen := make(map[string]bool)
	methodLines := make(map[int]bool) // track lines already extracted as class methods

	// --- Modules ---
	matches, _ := parser.RunQuery(qRbModule, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["mod.name"].Text
		def := m.Captures["mod.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindPackage, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Class methods (before top-level methods so we can skip them) ---
	// Instance methods first, then singleton methods (def self.x) under
	// the same path — both belong to the class but have different AST
	// shapes. Without this second pass, Rails-style `User.authenticate`
	// (a self.method) is invisible to search and to any graph query.
	for _, q := range []string{qRbClassMethod, qRbSingletonMethod} {
		matches, _ = parser.RunQuery(q, e.lang, root, src)
		for _, m := range matches {
			className := m.Captures["class.name"].Text
			methodName := m.Captures["method.name"].Text
			def := m.Captures["method.def"]

			id := filePath + "::" + className + "." + methodName
			if seen[id] {
				continue
			}
			seen[id] = true
			methodLines[def.StartLine] = true

			result.Nodes = append(result.Nodes, &graph.Node{
				ID: id, Kind: graph.KindMethod, Name: methodName,
				FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
				Language: "ruby", Meta: map[string]any{
					"receiver":  className,
					"signature": "def " + methodName,
				},
			})
			result.Edges = append(result.Edges, &graph.Edge{
				From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
			})
			typeID := filePath + "::" + className
			result.Edges = append(result.Edges, &graph.Edge{
				From: id, To: typeID, Kind: graph.EdgeMemberOf, FilePath: filePath, Line: def.StartLine + 1,
			})
		}
	}

	// --- Classes ---
	matches, _ = parser.RunQuery(qRbClass, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["class.name"].Text
		def := m.Captures["class.def"]
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindType, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Top-level methods (skip lines already extracted as class methods) ---
	matches, _ = parser.RunQuery(qRbMethod, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["method.name"].Text
		def := m.Captures["method.def"]
		if methodLines[def.StartLine] {
			continue
		}
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindFunction, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby", Meta: map[string]any{"signature": "def " + name},
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// --- Imports (require / require_relative) ---
	matches, _ = parser.RunQuery(qRbRequire, e.lang, root, src)
	for _, m := range matches {
		method := m.Captures["req.method"].Text
		if method != "require" && method != "require_relative" {
			continue
		}
		path := m.Captures["req.path"]
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: "unresolved::import::" + path.Text,
			Kind: graph.EdgeImports, FilePath: filePath, Line: path.StartLine + 1,
		})
	}

	// --- Call sites ---
	funcRanges := buildFuncRanges(result)

	matches, _ = parser.RunQuery(qRbCall, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["call.name"].Text
		expr := m.Captures["call.expr"]
		// Skip require/require_relative — already handled as imports.
		if name == "require" || name == "require_relative" {
			continue
		}
		callerID := findEnclosingFunc(funcRanges, expr.StartLine+1)
		if callerID == "" {
			continue
		}

		// Check if the call has a receiver (obj.method style).
		callNode := expr.Node
		var target string
		if callNode != nil {
			receiver := callNode.ChildByFieldName("receiver", e.lang)
			if receiver != nil {
				target = "unresolved::*." + name
			} else {
				target = "unresolved::" + name
			}
		} else {
			target = "unresolved::" + name
		}

		result.Edges = append(result.Edges, &graph.Edge{
			From: callerID, To: target,
			Kind: graph.EdgeCalls, FilePath: filePath, Line: expr.StartLine + 1,
		})
	}

	// --- Constants (uppercase assignments) ---
	matches, _ = parser.RunQuery(qRbAssignment, e.lang, root, src)
	for _, m := range matches {
		name := m.Captures["const.name"].Text
		def := m.Captures["const.def"]
		// Ruby constants start with an uppercase letter.
		if len(name) == 0 || !isUpperASCII(name[0]) {
			continue
		}
		id := filePath + "::" + name
		if seen[id] {
			continue
		}
		seen[id] = true
		result.Nodes = append(result.Nodes, &graph.Node{
			ID: id, Kind: graph.KindVariable, Name: name,
			FilePath: filePath, StartLine: def.StartLine + 1, EndLine: def.EndLine + 1,
			Language: "ruby",
		})
		result.Edges = append(result.Edges, &graph.Edge{
			From: fileNode.ID, To: id, Kind: graph.EdgeDefines, FilePath: filePath, Line: def.StartLine + 1,
		})
	}

	// Rails-style callback dispatch: before_action / after_action /
	// around_action / skip_before_action / before_filter (legacy) /
	// after_filter. These declarations bind callback methods to
	// controller actions — runtime dispatch with no explicit call
	// site. For each match we emit one EdgeCalls per (action, callback)
	// pair so both `callers:callback` and `call_chain:action` answer
	// the way a Rails developer would expect.
	emitRailsCallbacks(root, src, filePath, result, e.lang)

	return result, nil
}

// railsCallbackMethods enumerates the Rails controller macros that
// bind callbacks to actions. `skip_*` is intentionally excluded —
// it removes an inherited binding, and correctly honouring it would
// require parent-class tracking that's out of scope for the first
// pass. The negative-space impact is small; the positive binding
// from the parent class still surfaces as an edge.
var railsCallbackMethods = map[string]struct{}{
	"before_action":  {},
	"after_action":   {},
	"around_action":  {},
	"before_filter":  {},
	"after_filter":   {},
	"around_filter":  {},
}

func emitRailsCallbacks(root *sitter.Node, src []byte, filePath string, result *parser.ExtractionResult, lang *sitter.Language) {
	// Walk every class body looking for top-level call expressions
	// whose method identifier matches a callback macro.
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if parser.NodeType(n, lang) == "class" {
			nameNode := n.ChildByFieldName("name", lang)
			if nameNode == nil {
				return
			}
			className := nameNode.Text(src)
			classID := filePath + "::" + className

			// Actions = instance methods of this class. Build a quick
			// map from method name to node ID so callbacks can be
			// resolved locally; avoids the resolver pass entirely for
			// this synthetic edge.
			methodIDs := make(map[string]string)
			var bodyStatements *sitter.Node
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c != nil && parser.NodeType(c, lang) == "body_statement" {
					bodyStatements = c
					break
				}
			}
			if bodyStatements == nil {
				return
			}
			// Collect methods first so callback macros can resolve
			// symbol names to concrete IDs.
			for i := 0; i < int(bodyStatements.NamedChildCount()); i++ {
				c := bodyStatements.NamedChild(i)
				if c == nil {
					continue
				}
				if parser.NodeType(c, lang) == "method" || parser.NodeType(c, lang) == "singleton_method" {
					nn := c.ChildByFieldName("name", lang)
					if nn == nil {
						continue
					}
					name := nn.Text(src)
					methodIDs[name] = filePath + "::" + className + "." + name
				}
			}
			// First pass: collect every callback method named anywhere
			// in the class's before/after/around macros. These must be
			// excluded from the action set of EVERY macro — otherwise
			// `before_action :a; before_action :b` ends up binding a
			// to guard b and vice versa.
			allCallbacks := make(map[string]struct{})
			for i := 0; i < int(bodyStatements.NamedChildCount()); i++ {
				c := bodyStatements.NamedChild(i)
				if c == nil || parser.NodeType(c, lang) != "call" {
					continue
				}
				methodNode := c.ChildByFieldName("method", lang)
				if methodNode == nil {
					continue
				}
				if _, ok := railsCallbackMethods[methodNode.Text(src)]; !ok {
					continue
				}
				args := c.ChildByFieldName("arguments", lang)
				if args == nil {
					continue
				}
				for i := 0; i < int(args.NamedChildCount()); i++ {
					arg := args.NamedChild(i)
					if arg != nil && parser.NodeType(arg, lang) == "simple_symbol" {
						allCallbacks[strings.TrimPrefix(arg.Text(src), ":")] = struct{}{}
					}
				}
			}
			// Second pass: emit edges.
			for i := 0; i < int(bodyStatements.NamedChildCount()); i++ {
				c := bodyStatements.NamedChild(i)
				if c == nil || parser.NodeType(c, lang) != "call" {
					continue
				}
				methodNode := c.ChildByFieldName("method", lang)
				if methodNode == nil {
					continue
				}
				macro := methodNode.Text(src)
				if _, ok := railsCallbackMethods[macro]; !ok {
					continue
				}
				args := c.ChildByFieldName("arguments", lang)
				if args == nil {
					continue
				}
				emitRailsCallbackEdges(args, src, filePath, int(c.StartPoint().Row)+1, classID, className, methodIDs, allCallbacks, macro, result, lang)
			}
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

// emitRailsCallbackEdges pulls symbol args out of a callback macro call,
// applies only:/except: filters against the class's action methods,
// and emits one EdgeCalls per (action, callback) pair. Class-level
// callbacks without only:/except: fan out to every action.
func emitRailsCallbackEdges(args *sitter.Node, src []byte, filePath string, line int, classID, className string, methodIDs map[string]string, allCallbacks map[string]struct{}, macro string, result *parser.ExtractionResult, lang *sitter.Language) {
	var callbackSyms []string
	onlyFilter := map[string]struct{}{}
	exceptFilter := map[string]struct{}{}
	hasOnly := false
	hasExcept := false
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		if arg == nil {
			continue
		}
		switch parser.NodeType(arg, lang) {
		case "simple_symbol":
			// `:name` — the most common form.
			sym := strings.TrimPrefix(arg.Text(src), ":")
			callbackSyms = append(callbackSyms, sym)
		case "pair":
			// `only: :show` or `except: [:a, :b]`.
			keyNode := arg.ChildByFieldName("key", lang)
			valNode := arg.ChildByFieldName("value", lang)
			if keyNode == nil || valNode == nil {
				continue
			}
			key := strings.TrimSuffix(strings.TrimPrefix(keyNode.Text(src), ":"), ":")
			target := &onlyFilter
			set := &hasOnly
			switch key {
			case "only":
				// use default onlyFilter
			case "except":
				target = &exceptFilter
				set = &hasExcept
			default:
				continue
			}
			for _, sym := range collectRubySymbols(valNode, src, lang) {
				(*target)[sym] = struct{}{}
			}
			if len(*target) > 0 {
				*set = true
			}
		case "hash":
			// Older Ruby fat-comma syntax (`only => :show`). Rare in
			// modern Rails; skip for simplicity.
		}
	}
	if len(callbackSyms) == 0 {
		return
	}

	// Resolve the actions this macro applies to.
	var applyTo []string
	for name := range methodIDs {
		if hasOnly {
			if _, ok := onlyFilter[name]; !ok {
				continue
			}
		}
		if hasExcept {
			if _, ok := exceptFilter[name]; ok {
				continue
			}
		}
		// Exclude ALL callback methods — a before_action can never
		// guard another before_action's method (Rails fires them all
		// sequentially, each bound to *actions*, not to each other).
		if _, isCallback := allCallbacks[name]; isCallback {
			continue
		}
		applyTo = append(applyTo, name)
	}
	if len(applyTo) == 0 {
		return
	}
	for _, cb := range callbackSyms {
		target := methodIDs[cb]
		if target == "" {
			// Inherited callback (defined on a parent class). Emit
			// an unresolved:: target and let the resolver find it by
			// name — works when the parent is in the same repo.
			target = "unresolved::" + cb
		}
		for _, action := range applyTo {
			actionID := methodIDs[action]
			if actionID == "" {
				continue
			}
			result.Edges = append(result.Edges, &graph.Edge{
				From:     actionID,
				To:       target,
				Kind:     graph.EdgeCalls,
				FilePath: filePath,
				Line:     line,
				Meta: map[string]any{
					"dispatch_macro": macro,
					"rails_callback": cb,
				},
			})
		}
	}
	_ = classID
	_ = className
}

// collectRubySymbols gathers bare symbol tokens from an expression that
// may be a single symbol (`:foo`) or an array of them (`[:a, :b]`).
func collectRubySymbols(n *sitter.Node, src []byte, lang *sitter.Language) []string {
	var out []string
	switch parser.NodeType(n, lang) {
	case "simple_symbol":
		out = append(out, strings.TrimPrefix(n.Text(src), ":"))
	case "array":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && parser.NodeType(c, lang) == "simple_symbol" {
				out = append(out, strings.TrimPrefix(c.Text(src), ":"))
			}
		}
	}
	return out
}

func isUpperASCII(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

// Ensure RubyExtractor satisfies the Extractor interface at compile time.
var _ parser.Extractor = (*RubyExtractor)(nil)
