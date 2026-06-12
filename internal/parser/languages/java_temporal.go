package languages

import (
	"strings"

	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// javaTemporalStartWorkflowName returns the workflow TYPE name a Temporal
// Java workflow-stub creation starts, or "". It recognises the two stub
// factory shapes:
//
//	client.newWorkflowStub(OrderWorkflow.class, options)   // typed   → "OrderWorkflow"
//	client.newUntypedWorkflowStub("OrderWorkflow")         // untyped → "OrderWorkflow"
//
// The stub's @WorkflowMethod call actually triggers the start, but the
// type (the class literal / string) is the canonical workflow name, which
// the resolver cross-resolves to the registered workflow — whose
// implementation may live in a Go repo. A `Foo.class` argument is reduced
// to its simple name ("Foo"), matching the Java SDK's default workflow
// type and the name a Go RegisterWorkflow would use.
func javaTemporalStartWorkflowName(callNode *sitter.Node, method string, src []byte) string {
	switch method {
	case "newWorkflowStub", "newUntypedWorkflowStub":
	default:
		return ""
	}
	if callNode == nil {
		return ""
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	var first *sitter.Node
	for i := 0; i < int(args.NamedChildCount()); i++ {
		if c := args.NamedChild(i); c != nil {
			first = c
			break
		}
	}
	if first == nil {
		return ""
	}
	text := first.Content(src)
	// `OrderWorkflow.class` / `com.example.OrderWorkflow.class` — robust to
	// the grammar representing the class literal as a class_literal or a
	// field_access by matching the trailing `.class`.
	if strings.HasSuffix(text, ".class") {
		return javaSimpleTypeName(strings.TrimSuffix(text, ".class"))
	}
	// `"OrderWorkflow"` — an untyped stub names the workflow by string.
	if first.Type() == "string_literal" {
		return strings.Trim(text, `"`)
	}
	return ""
}

// javaSimpleTypeName returns the trailing identifier of a possibly
// qualified Java type name (`com.example.Foo` → `Foo`).
func javaSimpleTypeName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}
