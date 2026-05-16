package languages

import (
	"strings"

	"github.com/zzet/gortex/internal/parser"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// emitSwiftAnnotationEdges scans a declaration node for `modifiers` /
// `attribute` children and emits one EdgeAnnotated per attribute onto
// the synthetic `annotation::swift::<name>` node. Covers:
//
//	@objc                       → annotation::swift::objc
//	@objc(legacyName)           → annotation::swift::objc (args="legacyName")
//	@available(iOS 13.0, *)     → annotation::swift::available
//	@MainActor                  → annotation::swift::MainActor
//	@Published                  → annotation::swift::Published
//	@State / @Binding / @Environment — SwiftUI property wrappers
//	@objcMembers, @inlinable, @inline, @dynamicCallable, @propertyWrapper, …
//
// Property wrappers and actor attributes are dispatch-relevant — they
// change how the property is accessed (KVO, observation framework,
// main-thread isolation) — so making them queryable via `find_usages`
// on the synthetic annotation node lets agents answer "every @Published
// property in this module" with one hop.
//
// The Swift tree-sitter grammar nests attributes under a `modifiers`
// child of the declaration, so the scan walks that level. If the
// declaration has no modifiers child the function is a silent no-op.
func emitSwiftAnnotationEdges(
	defNode *sitter.Node, fromID, filePath string, src []byte,
	result *parser.ExtractionResult, seen map[string]bool,
) {
	if defNode == nil || fromID == "" {
		return
	}
	mods := defNode.ChildByFieldName("modifiers")
	if mods == nil {
		// Some declarations expose modifiers as a positional named
		// child rather than via a named field; scan top-level
		// children for a `modifiers` node as a fallback.
		for i := 0; i < int(defNode.NamedChildCount()); i++ {
			c := defNode.NamedChild(i)
			if c != nil && c.Type() == "modifiers" {
				mods = c
				break
			}
		}
	}
	if mods == nil {
		return
	}

	for i := 0; i < int(mods.NamedChildCount()); i++ {
		attr := mods.NamedChild(i)
		if attr == nil || attr.Type() != "attribute" {
			continue
		}
		name, args := swiftAttributeNameAndArgs(attr, src)
		if name == "" {
			continue
		}
		line := int(attr.StartPoint().Row) + 1
		EmitAnnotationEdge(fromID, "swift", name, args, filePath, line, result, seen)
	}
}

// swiftAttributeNameAndArgs reads an `attribute` AST node and returns
// (name, args). The name comes from the first `user_type` /
// `type_identifier` child (Swift's grammar wraps attribute names in a
// type position). Arguments come from any remaining named children
// joined by ", " — the verbatim form is preserved so route paths and
// availability shims stay queryable.
//
// For qualified attribute names (`@SomeModule.SomeAttr`) the trailing
// segment is returned so the synthetic annotation node groups every
// equivalent use regardless of import alias.
func swiftAttributeNameAndArgs(attr *sitter.Node, src []byte) (string, string) {
	if attr == nil {
		return "", ""
	}
	var name string
	var argParts []string
	for i := 0; i < int(attr.NamedChildCount()); i++ {
		c := attr.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "user_type":
			if name == "" {
				name = swiftUserTypeName(c, src)
			}
		case "type_identifier", "simple_identifier", "identifier":
			if name == "" {
				name = strings.TrimSpace(c.Content(src))
			} else {
				argParts = append(argParts, strings.TrimSpace(c.Content(src)))
			}
		default:
			argParts = append(argParts, strings.TrimSpace(c.Content(src)))
		}
	}
	args := strings.Join(argParts, ", ")
	return name, args
}

// swiftUserTypeName pulls the trailing `type_identifier` out of a
// `user_type` chain (`Foo.Bar.Baz` → "Baz") so qualified annotation
// references collapse onto the same synthetic node.
func swiftUserTypeName(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	// Walk forward and remember the last type_identifier; Swift's
	// user_type nests left-to-right so the last identifier is the
	// leaf.
	var last string
	for i := 0; i < int(node.NamedChildCount()); i++ {
		c := node.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type() == "type_identifier" {
			last = strings.TrimSpace(c.Content(src))
		}
		if c.Type() == "user_type" {
			if inner := swiftUserTypeName(c, src); inner != "" {
				last = inner
			}
		}
	}
	if last == "" {
		// Fallback: take the verbatim content and slice on the last
		// `.` separator so qualified names still surface a leaf.
		text := strings.TrimSpace(node.Content(src))
		if idx := strings.LastIndex(text, "."); idx >= 0 {
			text = text[idx+1:]
		}
		last = text
	}
	return last
}
