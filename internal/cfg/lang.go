package cfg

import (
	"strings"

	sitter "github.com/zzet/gortex/internal/parser/tsitter"
	golang "github.com/zzet/gortex/internal/parser/tsitter/golang"
	javalang "github.com/zzet/gortex/internal/parser/tsitter/java"
	jslang "github.com/zzet/gortex/internal/parser/tsitter/javascript"
	pylang "github.com/zzet/gortex/internal/parser/tsitter/python"
	rubylang "github.com/zzet/gortex/internal/parser/tsitter/ruby"
	rustlang "github.com/zzet/gortex/internal/parser/tsitter/rust"
	tsxlang "github.com/zzet/gortex/internal/parser/tsitter/tsx"
	tslang "github.com/zzet/gortex/internal/parser/tsitter/typescript"
)

// assignMode says whether an assignment-shaped construct also reads
// its targets before writing them.
type assignMode int

const (
	augNever  assignMode = iota // plain assignment / declaration
	augAlways                   // augmented assign (x += 1): def + use
	augIfOp                     // augmented iff the operator token isn't bare "="
)

// assignRule describes one assignment-shaped node kind: which field
// holds the write targets and whether the targets are also read.
type assignRule struct {
	lhsField string
	mode     assignMode
}

// updateRule describes increment/decrement-shaped nodes whose single
// target is both read and written. An empty field means "first named
// child".
type updateRule struct {
	field string
}

// param is one parameter binding discovered in a function header.
type param struct {
	name string
	line int
}

// langSpec is the per-language table driving CFG construction and
// def/use extraction. The shared builder owns all block/edge
// mechanics; the spec only names the AST shapes.
type langSpec struct {
	name      string
	grammar   func() *sitter.Language
	classWrap bool // retry parse inside `class __gortexcfg__ { … }`
	dedent    bool // strip common indentation before parsing

	funcKinds map[string]bool
	// dispatch consumes control constructs; returning false makes the
	// builder record the node as a leaf statement.
	dispatch func(b *builder, n *sitter.Node) bool

	identKinds        map[string]bool
	assigns           map[string]assignRule
	updates           map[string]updateRule
	skipFields        map[string]map[string]bool
	skipKinds         map[string]bool
	nestedFuncs       map[string]bool
	patternContainers map[string]bool
	paramSkipFields   map[string]bool
	paramSkipKinds    map[string]bool
}

// bodyOf locates a function node's body, falling back from the
// `body` field to the language's known body node kind (Ruby methods
// carry an unfielded body_statement in some grammar shapes).
func (s *langSpec) bodyOf(fn *sitter.Node) *sitter.Node {
	if b := fn.ChildByFieldName("body"); b != nil {
		return b
	}
	if s.name == "ruby" {
		return childOfType(fn, "body_statement")
	}
	return nil
}

// params collects the function's parameter bindings (including the
// Go receiver) as block-0 definitions.
func (s *langSpec) params(fn *sitter.Node, src []byte) []param {
	var out []param
	collect := func(container *sitter.Node) {
		if container == nil {
			return
		}
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			if s.identKinds[n.Type()] {
				name := n.Content(src)
				if name != "" && name != "_" {
					out = append(out, param{name: name, line: int(n.StartPoint().Row) + 1})
				}
				return
			}
			if s.paramSkipKinds[n.Type()] {
				return
			}
			cnt := int(n.ChildCount())
			for i := 0; i < cnt; i++ {
				c := n.Child(i)
				if c == nil || !c.IsNamed() {
					continue
				}
				if f := n.FieldNameForChild(i); f != "" && s.paramSkipFields[f] {
					continue
				}
				walk(c)
			}
		}
		walk(container)
	}
	if s.name == "go" {
		collect(fn.ChildByFieldName("receiver"))
	}
	if p := fn.ChildByFieldName("parameters"); p != nil {
		collect(p)
	} else if p := fn.ChildByFieldName("parameter"); p != nil {
		// JS arrow functions with a single unparenthesized parameter.
		collect(p)
	}
	return out
}

// specFor maps a graph language label to its spec. Returns nil for
// languages without a control-construct table.
func specFor(language string) *langSpec {
	switch strings.ToLower(language) {
	case "go", "golang":
		return goSpec
	case "python", "py":
		return pySpec
	case "javascript", "js", "jsx":
		return jsSpec
	case "typescript", "ts":
		return tsSpec
	case "tsx":
		return tsxSpec
	case "java":
		return javaSpec
	case "rust", "rs":
		return rustSpec
	case "ruby", "rb":
		return rubySpec
	}
	return nil
}

// SupportedLanguage reports whether Build can construct a CFG for
// the given graph language label.
func SupportedLanguage(language string) bool { return specFor(language) != nil }

// childOfType returns the first named child with the given type.
func childOfType(n *sitter.Node, kind string) *sitter.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type() == kind {
			return c
		}
	}
	return nil
}

// childrenByField returns every child stored under the given field
// name, in order. tree-sitter allows repeated fields (Python's
// if_statement stacks elif/else clauses under `alternative`).
func childrenByField(n *sitter.Node, field string) []*sitter.Node {
	var out []*sitter.Node
	cnt := int(n.ChildCount())
	for i := 0; i < cnt; i++ {
		if n.FieldNameForChild(i) == field {
			if c := n.Child(i); c != nil {
				out = append(out, c)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Go
// ---------------------------------------------------------------------------

var goSpec = &langSpec{
	name:    "go",
	grammar: golang.GetLanguage,
	funcKinds: map[string]bool{
		"function_declaration": true, "method_declaration": true, "func_literal": true,
	},
	identKinds: map[string]bool{"identifier": true},
	assigns: map[string]assignRule{
		"short_var_declaration": {lhsField: "left", mode: augNever},
		"assignment_statement":  {lhsField: "left", mode: augIfOp},
		"var_spec":              {lhsField: "name", mode: augNever},
		"const_spec":            {lhsField: "name", mode: augNever},
		"range_clause":          {lhsField: "left", mode: augNever},
		"receive_statement":     {lhsField: "left", mode: augNever},
	},
	updates: map[string]updateRule{
		"inc_statement": {}, "dec_statement": {},
	},
	skipFields: map[string]map[string]bool{
		"selector_expression": {"field": true},
		"keyed_element":       {"key": true},
	},
	skipKinds: map[string]bool{
		"field_identifier": true, "type_identifier": true, "package_identifier": true,
		"label_name": true,
	},
	nestedFuncs:       map[string]bool{"func_literal": true},
	patternContainers: map[string]bool{"expression_list": true},
	paramSkipFields:   map[string]bool{"type": true},
	paramSkipKinds:    map[string]bool{"type_identifier": true, "qualified_type": true},
	dispatch:          goDispatch,
}

func goDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "block", "statement_list":
		b.buildSeq(n)
	case "if_statement":
		b.buildIf(n.ChildByFieldName("initializer"), n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "for_statement":
		body := n.ChildByFieldName("body")
		if fc := childOfType(n, "for_clause"); fc != nil {
			b.buildLoop(loopParts{
				init:   fc.ChildByFieldName("initializer"),
				cond:   fc.ChildByFieldName("condition"),
				update: fc.ChildByFieldName("update"),
				body:   body,
			})
			return true
		}
		if rc := childOfType(n, "range_clause"); rc != nil {
			b.buildLoop(loopParts{headerStmt: rc, body: body})
			return true
		}
		// `for cond { … }` — the condition is the lone named child
		// that isn't the body; bare `for { … }` has none.
		var cond *sitter.Node
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && !c.Equal(body) && c.Type() != "comment" {
				cond = c
				break
			}
		}
		b.buildLoop(loopParts{cond: cond, body: body, infinite: cond == nil})
	case "expression_switch_statement", "type_switch_statement", "select_statement":
		b.buildGoSwitch(n)
	case "labeled_statement":
		if lbl := childOfType(n, "label_name"); lbl != nil {
			b.pendingLabel = lbl.Content(b.src)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type() != "label_name" {
				b.buildStmt(c)
			}
		}
		b.pendingLabel = ""
	case "break_statement":
		b.buildBreak(n, goJumpLabel(n, b.src))
	case "continue_statement":
		b.buildContinue(n, goJumpLabel(n, b.src))
	case "return_statement":
		b.buildReturn(n, "return", LabelReturn)
	case "fallthrough_statement":
		b.leaf(n, "fallthrough")
		b.pendingFallthrough = true
	case "defer_statement":
		// Defers run at function exit; they do not alter intra-block
		// flow, so the statement is recorded in place and flagged.
		b.leaf(n, "defer")
	case "go_statement":
		b.leaf(n, "go")
	default:
		return false
	}
	return true
}

// goJumpLabel extracts the optional label off a break/continue.
func goJumpLabel(n *sitter.Node, src []byte) string {
	if lbl := childOfType(n, "label_name"); lbl != nil {
		return lbl.Content(src)
	}
	return ""
}

// buildGoSwitch covers expression/type switches and select: every
// case is dispatched from the head, cases do not fall through unless
// an explicit fallthrough statement was seen.
func (b *builder) buildGoSwitch(n *sitter.Node) {
	b.ensureCur()
	if init := n.ChildByFieldName("initializer"); init != nil {
		b.buildStmt(init)
	}
	if alias := n.ChildByFieldName("alias"); alias != nil {
		// Type switch `switch v := x.(type)`: the alias binds v as a
		// definition, the switched value field is a read. (The grammar
		// always exposes `value` for the subject, so the binding lives
		// in the alias field, not value.)
		st := b.recordNode(alias, "cond")
		st.Defs, _ = extractDefUse(b.spec, b.src, alias, true)
		if v := n.ChildByFieldName("value"); v != nil {
			_, st.Uses = extractDefUse(b.spec, b.src, v, false)
		}
	} else if v := n.ChildByFieldName("value"); v != nil {
		b.addStmt(v, "cond")
	}
	head := b.cur
	after := b.newBlock("switch_end")
	// Go `break` (labeled or bare) exits the switch/select, not the
	// enclosing loop. Push a break-only frame (no continue target, so
	// `continue` still skips past it to the loop) and consume any
	// pending label from an enclosing labeled statement.
	b.pushFrame(frame{label: b.takeLabel(), breakTo: after})
	defer b.popFrame()
	hasDefault := false
	var pendingFT *Block
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "expression_case", "type_case", "communication_case", "default_case":
		default:
			continue
		}
		caseBlock := b.newBlock("case")
		b.edge(head, caseBlock, LabelCase)
		if pendingFT != nil {
			b.edge(pendingFT, caseBlock, LabelSeq)
			pendingFT = nil
		}
		b.cur = caseBlock
		if c.Type() == "default_case" {
			hasDefault = true
		} else if c.Type() == "communication_case" {
			if comm := c.ChildByFieldName("communication"); comm != nil {
				b.addStmt(comm, "case")
			}
		} else if v := c.ChildByFieldName("value"); v != nil {
			b.addStmt(v, "case")
		} else if tn := c.ChildByFieldName("type"); tn != nil {
			b.addStmt(tn, "case")
		}
		if sl := childOfType(c, "statement_list"); sl != nil {
			b.buildSeq(sl)
		}
		if b.pendingFallthrough {
			pendingFT = b.cur
			b.pendingFallthrough = false
			b.cur = nil
		}
		if b.cur != nil {
			b.edge(b.cur, after, LabelSeq)
		}
	}
	if !hasDefault {
		b.edge(head, after, LabelFalse)
	}
	b.cur = after
}

// ---------------------------------------------------------------------------
// Python
// ---------------------------------------------------------------------------

var pySpec = &langSpec{
	name:    "python",
	grammar: pylang.GetLanguage,
	dedent:  true,
	funcKinds: map[string]bool{
		"function_definition": true,
	},
	identKinds: map[string]bool{"identifier": true},
	assigns: map[string]assignRule{
		"assignment":           {lhsField: "left", mode: augNever},
		"augmented_assignment": {lhsField: "left", mode: augAlways},
		"named_expression":     {lhsField: "name", mode: augNever},
		"as_pattern":           {lhsField: "alias", mode: augNever},
	},
	updates: map[string]updateRule{},
	skipFields: map[string]map[string]bool{
		"attribute":        {"attribute": true},
		"keyword_argument": {"name": true},
	},
	skipKinds: map[string]bool{},
	nestedFuncs: map[string]bool{
		"function_definition": true, "lambda": true, "class_definition": true,
	},
	patternContainers: map[string]bool{
		"pattern_list": true, "tuple_pattern": true, "list_pattern": true,
		"as_pattern_target": true,
	},
	paramSkipFields: map[string]bool{"type": true, "value": true},
	paramSkipKinds:  map[string]bool{"type": true},
	dispatch:        pyDispatch,
}

func pyDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "block":
		b.buildSeq(n)
	case "if_statement":
		b.buildIfChain(n.ChildByFieldName("condition"), n.ChildByFieldName("consequence"),
			childrenByField(n, "alternative"))
	case "elif_clause":
		// Reached only via buildIfChain recursion fallback.
		b.buildIfChain(n.ChildByFieldName("condition"), n.ChildByFieldName("consequence"), nil)
	case "else_clause":
		if body := n.ChildByFieldName("body"); body != nil {
			b.buildStmt(body)
		} else {
			b.buildSeq(n)
		}
	case "while_statement":
		b.buildLoop(loopParts{
			cond:     n.ChildByFieldName("condition"),
			body:     n.ChildByFieldName("body"),
			elseNode: n.ChildByFieldName("alternative"),
		})
	case "for_statement":
		b.buildLoop(loopParts{
			headerStmt:                 n,
			headerStmtOnlyHeaderFields: true,
			body:                       n.ChildByFieldName("body"),
			elseNode:                   n.ChildByFieldName("alternative"),
		})
	case "try_statement":
		b.buildPyTry(n)
	case "with_statement":
		// `with` introduces bindings but no branching beyond the
		// (ignored) exception path already modeled by try blocks.
		if cl := childOfType(n, "with_clause"); cl != nil {
			b.leaf(cl, "with")
		}
		if body := n.ChildByFieldName("body"); body != nil {
			b.buildStmt(body)
		}
	case "break_statement":
		b.buildBreak(n, "")
	case "continue_statement":
		b.buildContinue(n, "")
	case "return_statement":
		b.buildReturn(n, "return", LabelReturn)
	case "raise_statement":
		b.buildReturn(n, "throw", LabelException)
	case "match_statement":
		b.buildPyMatch(n)
	default:
		return false
	}
	return true
}

// buildPyTry maps try/except/else/finally onto the generic try
// builder.
func (b *builder) buildPyTry(n *sitter.Node) {
	p := tryParts{bodyNode: n.ChildByFieldName("body")}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "except_clause", "except_group_clause":
			h := handlerPart{bodyNode: childOfType(c, "block")}
			if v := c.ChildByFieldName("value"); v != nil {
				h.headerNode = v
			}
			p.handlers = append(p.handlers, h)
		case "else_clause":
			p.elseNode = childOfType(c, "block")
		case "finally_clause":
			p.finallyNode = childOfType(c, "block")
		}
	}
	b.buildTry(p)
}

// buildPyMatch maps structural pattern matching onto the switch
// shape: every case is dispatched from the subject, no fallthrough.
func (b *builder) buildPyMatch(n *sitter.Node) {
	b.ensureCur()
	if subj := n.ChildByFieldName("subject"); subj != nil {
		b.addStmt(subj, "cond")
	}
	head := b.cur
	after := b.newBlock("match_end")
	// The case clauses live inside the match's body block (stored
	// under the `alternative` field), not as direct children of the
	// match_statement, so descend into the body before scanning.
	scope := n.ChildByFieldName("body")
	if scope == nil {
		scope = n
	}
	matchedAll := false
	for i := 0; i < int(scope.NamedChildCount()); i++ {
		c := scope.NamedChild(i)
		if c == nil || c.Type() != "case_clause" {
			continue
		}
		caseBlock := b.newBlock("case")
		b.edge(head, caseBlock, LabelCase)
		b.cur = caseBlock
		// The case pattern binds names (capture patterns) — record it
		// as a definition; the guard, when present, reads.
		if pat := childOfType(c, "case_pattern"); pat != nil {
			st := b.recordNode(pat, "case")
			st.Defs, _ = extractDefUse(b.spec, b.src, pat, true)
			if g := c.ChildByFieldName("guard"); g != nil {
				_, st.Uses = extractDefUse(b.spec, b.src, g, false)
			}
			if isPyWildcardCase(pat) {
				matchedAll = true
			}
		}
		if body := c.ChildByFieldName("consequence"); body != nil {
			b.buildStmt(body)
		}
		if b.cur != nil {
			b.edge(b.cur, after, LabelSeq)
		}
	}
	if !matchedAll {
		b.edge(head, after, LabelFalse)
	}
	b.cur = after
}

// isPyWildcardCase reports whether a case_pattern is the catch-all
// `case _`: the grammar emits an empty case_pattern (no children) for
// the wildcard, so the subsequent arms are unreachable.
func isPyWildcardCase(pat *sitter.Node) bool {
	return pat.NamedChildCount() == 0
}

// ---------------------------------------------------------------------------
// JavaScript / TypeScript
// ---------------------------------------------------------------------------

func jsLikeSpec(name string, grammar func() *sitter.Language) *langSpec {
	return &langSpec{
		name:      name,
		grammar:   grammar,
		classWrap: true,
		funcKinds: map[string]bool{
			"function_declaration": true, "function_expression": true, "function": true,
			"generator_function_declaration": true, "generator_function": true,
			"method_definition": true, "arrow_function": true,
		},
		identKinds: map[string]bool{
			"identifier": true, "shorthand_property_identifier_pattern": true,
		},
		assigns: map[string]assignRule{
			"variable_declarator":             {lhsField: "name", mode: augNever},
			"assignment_expression":           {lhsField: "left", mode: augNever},
			"augmented_assignment_expression": {lhsField: "left", mode: augAlways},
			"assignment_pattern":              {lhsField: "left", mode: augNever},
		},
		updates: map[string]updateRule{
			"update_expression": {field: "argument"},
		},
		skipFields: map[string]map[string]bool{
			"member_expression": {"property": true},
			"pair":              {"key": true},
			"pair_pattern":      {"key": true},
		},
		skipKinds: map[string]bool{
			"property_identifier": true, "statement_identifier": true,
			"type_annotation": true, "type_identifier": true, "predefined_type": true,
		},
		nestedFuncs: map[string]bool{
			"function_declaration": true, "function_expression": true, "function": true,
			"generator_function_declaration": true, "generator_function": true,
			"method_definition": true, "arrow_function": true, "class_declaration": true,
			"class": true,
		},
		patternContainers: map[string]bool{
			"array_pattern": true, "object_pattern": true, "pair_pattern": true,
			"rest_pattern": true,
		},
		paramSkipFields: map[string]bool{"type": true, "value": true, "right": true},
		paramSkipKinds:  map[string]bool{"type_annotation": true},
		dispatch:        jsDispatch,
	}
}

var (
	jsSpec  = jsLikeSpec("javascript", jslang.GetLanguage)
	tsSpec  = jsLikeSpec("typescript", tslang.GetLanguage)
	tsxSpec = jsLikeSpec("tsx", tsxlang.GetLanguage)
)

func jsDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "statement_block":
		b.buildSeq(n)
	case "if_statement":
		b.buildIf(nil, n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "else_clause":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c != nil {
				b.buildStmt(c)
			}
		}
	case "while_statement":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body")})
	case "do_statement":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body"), postTest: true})
	case "for_statement":
		b.buildLoop(loopParts{
			init:   n.ChildByFieldName("initializer"),
			cond:   n.ChildByFieldName("condition"),
			update: n.ChildByFieldName("increment"),
			body:   n.ChildByFieldName("body"),
		})
	case "for_in_statement":
		b.buildLoop(loopParts{headerStmt: n, headerStmtOnlyHeaderFields: true, body: n.ChildByFieldName("body")})
	case "switch_statement":
		b.buildJsSwitch(n)
	case "try_statement":
		p := tryParts{bodyNode: n.ChildByFieldName("body")}
		if h := n.ChildByFieldName("handler"); h != nil {
			p.handlers = append(p.handlers, handlerPart{
				headerNode: h.ChildByFieldName("parameter"),
				headerDefs: true,
				bodyNode:   h.ChildByFieldName("body"),
			})
		}
		if f := n.ChildByFieldName("finalizer"); f != nil {
			p.finallyNode = f.ChildByFieldName("body")
		}
		b.buildTry(p)
	case "labeled_statement":
		if lbl := n.ChildByFieldName("label"); lbl != nil {
			b.pendingLabel = lbl.Content(b.src)
		}
		if body := n.ChildByFieldName("body"); body != nil {
			b.buildStmt(body)
		}
		b.pendingLabel = ""
	case "break_statement":
		b.buildBreak(n, fieldText(n, "label", b.src))
	case "continue_statement":
		b.buildContinue(n, fieldText(n, "label", b.src))
	case "return_statement":
		b.buildReturn(n, "return", LabelReturn)
	case "throw_statement":
		b.buildReturn(n, "throw", LabelException)
	default:
		return false
	}
	return true
}

// buildJsSwitch models C-style fallthrough: consecutive cases chain
// unless a break/return terminated the previous one; break targets
// the switch end. Java arrow rules (`case 1 -> …`) never fall
// through, so they bypass the fallthrough chaining and edge straight
// to the switch end.
func (b *builder) buildJsSwitch(n *sitter.Node) {
	b.ensureCur()
	if v := n.ChildByFieldName("value"); v != nil {
		b.addStmt(v, "cond")
	} else if v := n.ChildByFieldName("condition"); v != nil {
		b.addStmt(v, "cond")
	}
	head := b.cur
	after := b.newBlock("switch_end")
	b.pushFrame(frame{breakTo: after})
	hasDefault := false
	var prevEnd *Block
	body := n.ChildByFieldName("body")
	if body == nil {
		body = n
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		if c == nil {
			continue
		}
		var isDefault, isArrowRule bool
		switch c.Type() {
		case "switch_case":
		case "switch_default":
			isDefault = true
		case "switch_block_statement_group":
			isDefault = javaGroupIsDefault(c)
		case "switch_rule":
			// Arrow form: `case 1 -> { … }`. No fallthrough.
			isDefault = javaGroupIsDefault(c)
			isArrowRule = true
		default:
			continue
		}
		caseBlock := b.newBlock("case")
		b.edge(head, caseBlock, LabelCase)
		if prevEnd != nil && !isArrowRule {
			b.edge(prevEnd, caseBlock, LabelSeq)
		}
		b.cur = caseBlock
		if isDefault {
			hasDefault = true
		}
		b.buildCaseGroupBody(c)
		if isArrowRule {
			// Implicit break: the rule's end leaves the switch directly
			// and does not chain into the next rule.
			if b.cur != nil {
				b.edge(b.cur, after, LabelSeq)
			}
			prevEnd = nil
			continue
		}
		prevEnd = b.cur
	}
	b.popFrame()
	if prevEnd != nil {
		b.edge(prevEnd, after, LabelSeq)
	}
	if !hasDefault {
		b.edge(head, after, LabelFalse)
	}
	b.cur = after
}

// buildCaseGroupBody emits the case-label match values then the
// case's statements. Works for JS switch_case/switch_default (value
// field + repeated body fields) and Java switch_block_statement_group
// (switch_label children followed by statements).
func (b *builder) buildCaseGroupBody(c *sitter.Node) {
	if v := c.ChildByFieldName("value"); v != nil {
		b.addStmt(v, "case")
	}
	cnt := int(c.ChildCount())
	for i := 0; i < cnt; i++ {
		ch := c.Child(i)
		if ch == nil || !ch.IsNamed() {
			continue
		}
		f := c.FieldNameForChild(i)
		if f == "value" {
			continue
		}
		if ch.Type() == "switch_label" {
			if ch.NamedChildCount() > 0 {
				b.addStmt(ch, "case")
			}
			continue
		}
		if f == "body" || f == "" {
			b.buildStmt(ch)
		}
	}
}

// javaGroupIsDefault reports whether a Java case group carries the
// `default` label (a switch_label with no children).
func javaGroupIsDefault(c *sitter.Node) bool {
	for i := 0; i < int(c.NamedChildCount()); i++ {
		ch := c.NamedChild(i)
		if ch != nil && ch.Type() == "switch_label" && ch.NamedChildCount() == 0 {
			return true
		}
	}
	return false
}

func fieldText(n *sitter.Node, field string, src []byte) string {
	if c := n.ChildByFieldName(field); c != nil {
		return c.Content(src)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Java
// ---------------------------------------------------------------------------

var javaSpec = &langSpec{
	name:      "java",
	grammar:   javalang.GetLanguage,
	classWrap: true,
	funcKinds: map[string]bool{
		"method_declaration": true, "constructor_declaration": true,
	},
	identKinds: map[string]bool{"identifier": true},
	assigns: map[string]assignRule{
		"variable_declarator":   {lhsField: "name", mode: augNever},
		"assignment_expression": {lhsField: "left", mode: augIfOp},
		"resource":              {lhsField: "name", mode: augNever},
	},
	updates: map[string]updateRule{
		"update_expression": {},
	},
	skipFields: map[string]map[string]bool{
		"field_access":      {"field": true},
		"method_invocation": {"name": true},
		"method_reference":  {},
	},
	skipKinds: map[string]bool{
		"type_identifier": true, "integral_type": true, "floating_point_type": true,
		"boolean_type": true, "void_type": true, "generic_type": true,
		"annotation": true, "marker_annotation": true, "modifiers": true,
	},
	nestedFuncs: map[string]bool{
		"lambda_expression": true, "class_declaration": true, "anonymous_class_body": true,
	},
	patternContainers: map[string]bool{},
	paramSkipFields:   map[string]bool{"type": true, "dimensions": true},
	paramSkipKinds: map[string]bool{
		"type_identifier": true, "integral_type": true, "floating_point_type": true,
		"boolean_type": true, "generic_type": true, "annotation": true,
		"marker_annotation": true, "modifiers": true,
	},
	dispatch: javaDispatch,
}

func javaDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "block":
		b.buildSeq(n)
	case "if_statement":
		b.buildIf(nil, n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "while_statement":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body")})
	case "do_statement":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body"), postTest: true})
	case "for_statement":
		b.buildLoop(loopParts{
			init:   n.ChildByFieldName("init"),
			cond:   n.ChildByFieldName("condition"),
			update: n.ChildByFieldName("update"),
			body:   n.ChildByFieldName("body"),
		})
	case "enhanced_for_statement":
		b.buildLoop(loopParts{headerStmt: n, headerStmtOnlyHeaderFields: true, body: n.ChildByFieldName("body")})
	case "switch_expression", "switch_statement":
		b.buildJsSwitch(n)
	case "try_statement", "try_with_resources_statement":
		p := tryParts{bodyNode: n.ChildByFieldName("body")}
		if res := n.ChildByFieldName("resources"); res != nil {
			b.ensureCur()
			b.addStmt(res, "with")
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "catch_clause":
				h := handlerPart{bodyNode: c.ChildByFieldName("body"), headerDefs: true}
				if fp := childOfType(c, "catch_formal_parameter"); fp != nil {
					h.headerNode = fp
				}
				p.handlers = append(p.handlers, h)
			case "finally_clause":
				p.finallyNode = childOfType(c, "block")
			}
		}
		b.buildTry(p)
	case "labeled_statement":
		// (identifier) ':' statement — no fields in this grammar.
		first := n.NamedChild(0)
		if first != nil && first.Type() == "identifier" {
			b.pendingLabel = first.Content(b.src)
		}
		for i := 1; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c != nil {
				b.buildStmt(c)
			}
		}
		b.pendingLabel = ""
	case "break_statement":
		b.buildBreak(n, javaJumpLabel(n, b.src))
	case "continue_statement":
		b.buildContinue(n, javaJumpLabel(n, b.src))
	case "return_statement":
		b.buildReturn(n, "return", LabelReturn)
	case "throw_statement":
		b.buildReturn(n, "throw", LabelException)
	case "synchronized_statement":
		if l := n.ChildByFieldName("lock"); l != nil {
			b.ensureCur()
			b.addStmt(l, "")
		}
		if body := n.ChildByFieldName("body"); body != nil {
			b.buildStmt(body)
		}
	default:
		return false
	}
	return true
}

func javaJumpLabel(n *sitter.Node, src []byte) string {
	if c := n.NamedChild(0); c != nil && c.Type() == "identifier" {
		return c.Content(src)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Rust
// ---------------------------------------------------------------------------

var rustSpec = &langSpec{
	name:    "rust",
	grammar: rustlang.GetLanguage,
	funcKinds: map[string]bool{
		"function_item": true, "closure_expression": true,
	},
	identKinds: map[string]bool{"identifier": true},
	assigns: map[string]assignRule{
		"let_declaration":          {lhsField: "pattern", mode: augNever},
		"assignment_expression":    {lhsField: "left", mode: augNever},
		"compound_assignment_expr": {lhsField: "left", mode: augAlways},
		"let_condition":            {lhsField: "pattern", mode: augNever},
	},
	updates: map[string]updateRule{},
	skipFields: map[string]map[string]bool{
		"field_expression":     {"field": true},
		"tuple_struct_pattern": {"type": true},
		"struct_pattern":       {"type": true},
		"field_initializer":    {"field": true},
		// A match arm's guard hangs off the pattern node as the
		// `condition` field; it reads variables, it doesn't bind them,
		// so the binding walk must skip it.
		"match_pattern": {"condition": true},
	},
	skipKinds: map[string]bool{
		"type_identifier": true, "primitive_type": true, "field_identifier": true,
		"scoped_identifier": true, "scoped_type_identifier": true, "lifetime": true,
		"type_arguments": true, "label": true,
	},
	nestedFuncs: map[string]bool{
		"closure_expression": true, "function_item": true,
	},
	patternContainers: map[string]bool{
		"tuple_pattern": true, "tuple_struct_pattern": true, "struct_pattern": true,
		"slice_pattern": true, "reference_pattern": true, "mut_pattern": true,
		"field_pattern": true, "or_pattern": true, "match_pattern": true,
	},
	paramSkipFields: map[string]bool{"type": true},
	paramSkipKinds:  map[string]bool{"type_identifier": true, "primitive_type": true, "reference_type": true},
	dispatch:        rustDispatch,
}

func rustDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "block":
		b.buildSeq(n)
	case "expression_statement":
		// Unwrap so control-flow expressions in statement position
		// (if/while/match/loop) reach the cases below.
		if c := n.NamedChild(0); c != nil {
			b.buildStmt(c)
			return true
		}
		return false
	case "if_expression":
		b.buildIf(nil, n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "else_clause":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if c := n.NamedChild(i); c != nil {
				b.buildStmt(c)
			}
		}
	case "while_expression":
		b.pendingLabel = rustLoopOwnLabel(n, b.src)
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body")})
	case "loop_expression":
		b.pendingLabel = rustLoopOwnLabel(n, b.src)
		b.buildLoop(loopParts{body: n.ChildByFieldName("body"), infinite: true})
	case "for_expression":
		b.pendingLabel = rustLoopOwnLabel(n, b.src)
		b.buildLoop(loopParts{headerStmt: n, headerStmtOnlyHeaderFields: true, body: n.ChildByFieldName("body")})
	case "match_expression":
		b.buildRustMatch(n)
	case "break_expression":
		b.buildBreak(n, rustLoopLabel(n, b.src))
	case "continue_expression":
		b.buildContinue(n, rustLoopLabel(n, b.src))
	case "return_expression":
		b.buildReturn(n, "return", LabelReturn)
	default:
		return false
	}
	return true
}

// rustLoopLabel extracts the label off a break/continue expression
// (`break 'outer`). The vendored grammar exposes the label as a child
// of node type `label` whose first identifier is the lifetime name.
func rustLoopLabel(n *sitter.Node, src []byte) string {
	return rustLabelName(childOfType(n, "label"), src)
}

// rustLoopOwnLabel extracts the label a loop declares for itself
// (`'outer: loop { … }`). The label is a `label` child of the loop
// expression, ahead of the body.
func rustLoopOwnLabel(n *sitter.Node, src []byte) string {
	return rustLabelName(childOfType(n, "label"), src)
}

// rustLabelName normalises a `label` node to its bare name (no
// leading `'`). The label identifier is the node's first child; fall
// back to the node text for grammar shapes that inline it.
func rustLabelName(lbl *sitter.Node, src []byte) string {
	if lbl == nil {
		return ""
	}
	text := lbl.Content(src)
	if id := childOfType(lbl, "identifier"); id != nil {
		text = id.Content(src)
	}
	return strings.TrimPrefix(strings.TrimSpace(text), "'")
}

// buildRustMatch dispatches each arm off the subject; arms never
// fall through and the match is exhaustive, so there is no
// unmatched-subject edge.
func (b *builder) buildRustMatch(n *sitter.Node) {
	b.ensureCur()
	if v := n.ChildByFieldName("value"); v != nil {
		b.addStmt(v, "cond")
	}
	head := b.cur
	after := b.newBlock("match_end")
	body := n.ChildByFieldName("body")
	if body == nil {
		b.cur = after
		return
	}
	for i := 0; i < int(body.NamedChildCount()); i++ {
		arm := body.NamedChild(i)
		if arm == nil || arm.Type() != "match_arm" {
			continue
		}
		caseBlock := b.newBlock("case")
		b.edge(head, caseBlock, LabelCase)
		b.cur = caseBlock
		if pat := arm.ChildByFieldName("pattern"); pat != nil {
			// Arm patterns bind names; the guard (if any) reads. The
			// guard lives on the pattern node as its `condition` field
			// (skipped by the binding walk via skipFields), so the
			// asDef pass over the pattern yields only the captures.
			st := b.recordNode(pat, "case")
			defs, _ := extractDefUse(b.spec, b.src, pat, true)
			st.Defs = defs
			if g := pat.ChildByFieldName("condition"); g != nil {
				_, uses := extractDefUse(b.spec, b.src, g, false)
				st.Uses = uses
			}
		}
		if v := arm.ChildByFieldName("value"); v != nil {
			b.buildStmt(v)
		}
		if b.cur != nil {
			b.edge(b.cur, after, LabelSeq)
		}
	}
	b.cur = after
}

// ---------------------------------------------------------------------------
// Ruby
// ---------------------------------------------------------------------------

var rubySpec = &langSpec{
	name:    "ruby",
	grammar: rubylang.GetLanguage,
	funcKinds: map[string]bool{
		"method": true, "singleton_method": true,
	},
	identKinds: map[string]bool{"identifier": true},
	assigns: map[string]assignRule{
		"assignment":          {lhsField: "left", mode: augNever},
		"operator_assignment": {lhsField: "left", mode: augAlways},
	},
	updates: map[string]updateRule{},
	skipFields: map[string]map[string]bool{
		"call": {"method": true},
	},
	skipKinds: map[string]bool{
		"constant": true, "instance_variable": true, "class_variable": true,
		"global_variable": true, "symbol": true, "hash_key_symbol": true,
	},
	nestedFuncs: map[string]bool{
		"method": true, "singleton_method": true, "lambda": true,
		"do_block": true, "block": true, "class": true, "module": true,
	},
	patternContainers: map[string]bool{
		"left_assignment_list": true, "destructured_left_assignment": true,
		"rest_assignment": true,
	},
	paramSkipFields: map[string]bool{"value": true},
	paramSkipKinds:  map[string]bool{},
	dispatch:        rubyDispatch,
}

func rubyDispatch(b *builder, n *sitter.Node) bool {
	switch n.Type() {
	case "body_statement", "begin":
		b.buildRubyBody(n)
	case "then", "do", "else", "ensure":
		b.buildSeq(n)
	case "if", "unless":
		b.buildIf(nil, n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "elsif":
		b.buildIf(nil, n.ChildByFieldName("condition"),
			n.ChildByFieldName("consequence"), n.ChildByFieldName("alternative"))
	case "if_modifier", "unless_modifier":
		b.buildIf(nil, n.ChildByFieldName("condition"), n.ChildByFieldName("body"), nil)
	case "while", "until":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body")})
	case "while_modifier", "until_modifier":
		b.buildLoop(loopParts{cond: n.ChildByFieldName("condition"), body: n.ChildByFieldName("body")})
	case "for":
		b.buildLoop(loopParts{headerStmt: n, headerStmtOnlyHeaderFields: true, body: n.ChildByFieldName("body")})
	case "case":
		b.buildRubyCase(n)
	case "call":
		blk := n.ChildByFieldName("block")
		if blk == nil {
			blk = childOfType(n, "do_block")
		}
		if blk == nil {
			blk = childOfType(n, "block")
		}
		if blk == nil {
			return false // plain call — leaf statement
		}
		b.buildRubyBlockCall(n, blk)
	case "break":
		b.buildBreak(n, "")
	case "next", "redo":
		b.buildContinue(n, "")
	case "return":
		b.buildReturn(n, "return", LabelReturn)
	case "raise", "throw":
		b.buildReturn(n, "throw", LabelException)
	default:
		return false
	}
	return true
}

// buildRubyBody handles statement sequences that may carry method-
// level or begin-level rescue/ensure/else clauses.
func (b *builder) buildRubyBody(n *sitter.Node) {
	var mainStmts []*sitter.Node
	var p tryParts
	hasClauses := false
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "rescue":
			hasClauses = true
			h := handlerPart{}
			if v := c.ChildByFieldName("variable"); v != nil {
				h.headerNode = v
				h.headerDefs = true
			} else if ex := c.ChildByFieldName("exceptions"); ex != nil {
				h.headerNode = ex
			}
			h.bodyNode = c.ChildByFieldName("body")
			p.handlers = append(p.handlers, h)
		case "ensure":
			hasClauses = true
			p.finallyNode = c
		case "else":
			hasClauses = true
			p.elseNode = c
		default:
			mainStmts = append(mainStmts, c)
		}
	}
	if !hasClauses {
		for _, st := range mainStmts {
			b.buildStmt(st)
		}
		return
	}
	p.bodyStmts = mainStmts
	b.buildTry(p)
}

// buildRubyCase dispatches each `when` off the subject; clauses
// never fall through.
func (b *builder) buildRubyCase(n *sitter.Node) {
	b.ensureCur()
	if v := n.ChildByFieldName("value"); v != nil {
		b.addStmt(v, "cond")
	}
	head := b.cur
	after := b.newBlock("case_end")
	hasElse := false
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "when", "in_clause":
			caseBlock := b.newBlock("case")
			b.edge(head, caseBlock, LabelCase)
			b.cur = caseBlock
			if pat := c.ChildByFieldName("pattern"); pat != nil {
				b.addStmt(pat, "case")
			}
			if body := c.ChildByFieldName("body"); body != nil {
				b.buildStmt(body)
			}
			if b.cur != nil {
				b.edge(b.cur, after, LabelSeq)
			}
		case "else":
			hasElse = true
			elseBlock := b.newBlock("case")
			b.edge(head, elseBlock, LabelCase)
			b.cur = elseBlock
			b.buildSeq(c)
			if b.cur != nil {
				b.edge(b.cur, after, LabelSeq)
			}
		}
	}
	if !hasElse {
		b.edge(head, after, LabelFalse)
	}
	b.cur = after
}

// buildRubyBlockCall models `receiver.each do |x| … end` as a loop:
// the call is the header (reads receiver + args), the block body may
// run zero or more times, and `next`/`break` behave like loop
// continue/break.
func (b *builder) buildRubyBlockCall(call, blk *sitter.Node) {
	b.ensureCur()
	header := b.newBlock("block_call")
	b.moveTo(header)
	// Uses come from the call minus the block body (the spec's
	// nestedFuncs already exclude do_block/block subtrees).
	b.addStmt(call, "loop")
	after := b.newBlock("block_end")
	bodyBlock := b.newBlock("block_body")
	b.edge(header, bodyBlock, LabelTrue)
	b.edge(header, after, LabelFalse)
	b.pushFrame(frame{label: b.takeLabel(), continueTo: header, breakTo: after, isLoop: true})
	b.cur = bodyBlock
	if params := blk.ChildByFieldName("parameters"); params != nil {
		st := b.recordNode(params, "param")
		defs, _ := extractDefUse(b.spec, b.src, params, true)
		st.Defs = defs
	}
	if body := blk.ChildByFieldName("body"); body != nil {
		// The block body is a body_statement; build it directly so
		// rescue clauses inside the block still work.
		b.buildStmt(body)
	}
	if b.cur != nil {
		b.edge(b.cur, header, LabelLoopBack)
	}
	b.popFrame()
	b.cur = after
}
