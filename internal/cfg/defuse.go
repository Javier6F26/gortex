package cfg

import (
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// extractDefUse walks one statement's subtree and returns the
// variable names it defines and reads, in source order, deduplicated.
// asDef seeds the walk in definition context (used for binding-only
// nodes: loop variables, catch parameters, match patterns).
//
// Nested function literals are opaque: their bodies run at an
// unknown later time, so neither their writes nor their reads are
// attributed to this statement. A nested definition that binds a
// name in the enclosing scope (Python `def inner`, JS `function g`)
// still defines that name.
func extractDefUse(spec *langSpec, src []byte, n *sitter.Node, asDef bool) (defs, uses []string) {
	x := &duExtractor{spec: spec, src: src, seenDef: map[string]bool{}, seenUse: map[string]bool{}}
	x.walk(n, asDef)
	return x.defs, x.uses
}

type duExtractor struct {
	spec    *langSpec
	src     []byte
	defs    []string
	uses    []string
	seenDef map[string]bool
	seenUse map[string]bool
}

func (x *duExtractor) addDef(name string) {
	if name == "" || name == "_" || x.seenDef[name] {
		return
	}
	x.seenDef[name] = true
	x.defs = append(x.defs, name)
}

func (x *duExtractor) addUse(name string) {
	if name == "" || name == "_" || x.seenUse[name] {
		return
	}
	x.seenUse[name] = true
	x.uses = append(x.uses, name)
}

// walk visits n in read context (asDef=false) or binding context.
func (x *duExtractor) walk(n *sitter.Node, asDef bool) {
	if n == nil {
		return
	}
	t := n.Type()

	if x.spec.nestedFuncs[t] {
		// A named nested definition binds its name in this scope.
		if nameNode := n.ChildByFieldName("name"); nameNode != nil && x.spec.identKinds[nameNode.Type()] {
			x.addDef(nameNode.Content(x.src))
		}
		return
	}
	if x.spec.skipKinds[t] {
		return
	}
	if x.spec.identKinds[t] {
		if asDef {
			x.addDef(n.Content(x.src))
		} else {
			x.addUse(n.Content(x.src))
		}
		return
	}
	if rule, ok := x.spec.assigns[t]; ok {
		x.handleAssign(n, rule)
		return
	}
	if rule, ok := x.spec.updates[t]; ok {
		x.handleUpdate(n, rule)
		return
	}
	x.walkChildren(n, asDef)
}

func (x *duExtractor) walkChildren(n *sitter.Node, asDef bool) {
	t := n.Type()
	skips := x.spec.skipFields[t]
	cnt := int(n.ChildCount())
	for i := 0; i < cnt; i++ {
		c := n.Child(i)
		if c == nil || !c.IsNamed() {
			continue
		}
		if skips != nil {
			if f := n.FieldNameForChild(i); f != "" && skips[f] {
				continue
			}
		}
		x.walk(c, asDef)
	}
}

// handleAssign processes an assignment-shaped node: the LHS field
// holds binding targets, every other child is read.
func (x *duExtractor) handleAssign(n *sitter.Node, rule assignRule) {
	alsoUse := false
	switch rule.mode {
	case augAlways:
		alsoUse = true
	case augIfOp:
		alsoUse = hasAugmentedOperator(n)
	}
	skips := x.spec.skipFields[n.Type()]
	cnt := int(n.ChildCount())
	for i := 0; i < cnt; i++ {
		c := n.Child(i)
		if c == nil || !c.IsNamed() {
			continue
		}
		f := n.FieldNameForChild(i)
		if skips != nil && f != "" && skips[f] {
			continue
		}
		if f == rule.lhsField {
			x.walkLHS(c, alsoUse)
			continue
		}
		// Type annotations and other non-value fields are pruned by
		// skipKinds inside the recursive walk.
		x.walk(c, false)
	}
}

// handleUpdate processes increment/decrement-shaped nodes whose
// target is both read and written.
func (x *duExtractor) handleUpdate(n *sitter.Node, rule updateRule) {
	var target *sitter.Node
	if rule.field != "" {
		target = n.ChildByFieldName(rule.field)
	}
	if target == nil {
		target = n.NamedChild(0)
	}
	if target != nil {
		x.walkLHS(target, true)
	}
}

// walkLHS classifies an assignment target: a bare identifier is a
// definition (plus a use for augmented assigns); pattern containers
// recurse; anything else (member access, index expression) reads its
// base — writing x.f or x[i] mutates the object, not the binding.
func (x *duExtractor) walkLHS(n *sitter.Node, alsoUse bool) {
	if n == nil {
		return
	}
	t := n.Type()
	if x.spec.identKinds[t] {
		name := n.Content(x.src)
		x.addDef(name)
		if alsoUse {
			x.addUse(name)
		}
		return
	}
	if x.spec.patternContainers[t] {
		skips := x.spec.skipFields[t]
		cnt := int(n.ChildCount())
		for i := 0; i < cnt; i++ {
			c := n.Child(i)
			if c == nil || !c.IsNamed() {
				continue
			}
			if skips != nil {
				if f := n.FieldNameForChild(i); f != "" && skips[f] {
					continue
				}
			}
			x.walkLHS(c, alsoUse)
			// Default values inside destructuring patterns are
			// handled by the assignment_pattern rule during the
			// recursive walk; nothing extra needed here.
		}
		return
	}
	if rule, ok := x.spec.assigns[t]; ok && t == "assignment_pattern" {
		// Destructuring default: `{a = 1}` — a is a def, 1 is read.
		if l := n.ChildByFieldName(rule.lhsField); l != nil {
			x.walkLHS(l, alsoUse)
		}
		if r := n.ChildByFieldName("right"); r != nil {
			x.walk(r, false)
		}
		return
	}
	// Non-binding target: reads flow normally.
	x.walk(n, false)
}

// hasAugmentedOperator reports whether an assignment node carries a
// compound operator token (+=, -=, &&=, …) rather than plain "=".
func hasAugmentedOperator(n *sitter.Node) bool {
	if op := n.ChildByFieldName("operator"); op != nil {
		return op.Type() != "="
	}
	cnt := int(n.ChildCount())
	for i := 0; i < cnt; i++ {
		c := n.Child(i)
		if c == nil || c.IsNamed() {
			continue
		}
		t := c.Type()
		if t == "=" {
			return false
		}
		if len(t) >= 2 && t[len(t)-1] == '=' {
			switch t {
			case "==", "!=", "<=", ">=":
				continue
			}
			return true
		}
	}
	return false
}
