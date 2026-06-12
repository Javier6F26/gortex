// Temporal Go SDK call attribution.
//
// Workflows orchestrate activities through a thin set of dispatch
// helpers exposed by `go.temporal.io/sdk/workflow`:
//
//	workflow.ExecuteActivity(ctx, ActivityFn, args...)
//	workflow.ExecuteLocalActivity(ctx, ActivityFn, args...)
//	workflow.ExecuteChildWorkflow(ctx, WorkflowFn, args...)
//
// and activities / workflows enter the runtime via
// `go.temporal.io/sdk/worker`:
//
//	w.RegisterActivity(MyActivity)
//	w.RegisterActivityWithOptions(MyActivity, activity.RegisterOptions{Name: "..."})
//	w.RegisterWorkflow(MyWorkflow)
//	w.RegisterWorkflowWithOptions(MyWorkflow, workflow.RegisterOptions{Name: "..."})
//
// Tree-sitter sees `workflow.ExecuteActivity(...)` as a selector_expression
// call whose receiver text is "workflow" and method is the helper name;
// `w.RegisterActivity(...)` as a selector call whose method is the
// register helper. Neither shape resolves to anything useful through
// the normal Go call-resolution path (the target lives in an external
// SDK module). The helpers below recognise the call shapes and stamp
// dedicated `via=temporal.stub` / `via=temporal.register` placeholders
// that the resolver's ResolveTemporalCalls pass turns into edges from
// the workflow to the activity (or from one workflow to the child
// workflow) it dispatches.

package languages

import (
	"github.com/zzet/gortex/internal/graph"
	sitter "github.com/zzet/gortex/internal/parser/tsitter"
)

// goTemporalDispatchKind reports whether (receiver, method) names one
// of the Temporal workflow dispatch helpers and, if so, returns the
// canonical kind ("activity" or "workflow") plus whether the call is
// the `LocalActivity` variant. Returns ok=false for everything else.
//
// We require the receiver text to be exactly "workflow" — the
// canonical SDK alias. Users who alias the import (e.g.
// `import wf "go.temporal.io/sdk/workflow"`) won't be detected, which
// matches how the existing gRPC stub detector handles SDK aliasing
// (the canonical alias dominates >99% of real-world code).
func goTemporalDispatchKind(receiver, method string) (kind string, local bool, ok bool) {
	if receiver != "workflow" {
		return "", false, false
	}
	switch method {
	case "ExecuteActivity":
		return "activity", false, true
	case "ExecuteLocalActivity":
		return "activity", true, true
	case "ExecuteChildWorkflow":
		return "workflow", false, true
	}
	return "", false, false
}

// goTemporalRegisterKind reports whether a method name is one of the
// Temporal worker registration helpers and, if so, returns the kind
// ("activity" or "workflow") being registered. The receiver isn't
// required — `RegisterActivity` is distinctive enough across the SDK
// surface that a name match has zero realistic false positives.
//
// `RegisterActivities` (plural — registers every exported method on
// a struct as an activity) is recognised too; the resolver pass will
// promote each method of the struct to a temporal activity.
func goTemporalRegisterKind(method string) (kind string, plural bool, ok bool) {
	switch method {
	case "RegisterActivity", "RegisterActivityWithOptions":
		return "activity", false, true
	case "RegisterWorkflow", "RegisterWorkflowWithOptions":
		return "workflow", false, true
	case "RegisterActivities":
		return "activity", true, true
	}
	return "", false, false
}

// goTemporalDispatchArg returns the second positional argument node of a
// dispatch call (`workflow.ExecuteActivity(ctx, X, args...)` → X), or
// nil. X is either a string literal ("MyActivity"), a bare identifier
// (MyActivity), or a selector expression (pkg.MyActivity, recv.Method);
// goTemporalNameFromExpr reduces it to the trailing identifier — the
// name the worker registers under (the bare function name unless
// `RegisterActivityWithOptions` overrides it). Returned as a node, not a
// reduced name, so the env-default refinement can inspect the argument's
// shape (a bare identifier is the only case it tries to resolve to a
// literal default). Returns nil when the call has fewer than two
// positional arguments.
func goTemporalDispatchArg(callNode *sitter.Node) *sitter.Node {
	if callNode == nil || callNode.Type() != "call_expression" {
		return nil
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	count := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		count++
		if count == 2 {
			return c
		}
	}
	return nil
}

// goTemporalRegisterName extracts the registered function name from a
// `worker.RegisterActivity(F)` / `worker.RegisterWorkflow(F)` call —
// the first positional argument, which is the function reference.
// Same expression shapes as the dispatch-name argument.
func goTemporalRegisterName(callNode *sitter.Node, src []byte) string {
	if callNode == nil || callNode.Type() != "call_expression" {
		return ""
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		return goTemporalNameFromExpr(c, src)
	}
	return ""
}

// applyGoTemporalRegisterMeta stamps `via=temporal.register` plus
// `temporal_kind` (activity / workflow) and `temporal_name` (the
// function-reference identifier) onto an EdgeCalls edge derived from
// a Temporal worker-registration call. No-op when c.tempKind isn't
// the "register_*" form set by goTemporalRegisterKind.
//
// The resolver's ResolveTemporalCalls pass walks every edge carrying
// this meta to discover (name → registered function) pairs, then
// stamps `temporal_role` on the registered function nodes and uses
// the map to rewrite matching stub-call placeholders.
func applyGoTemporalRegisterMeta(edge *graph.Edge, c goDeferredCall) {
	if edge == nil || c.tempKind == "" || c.tempName == "" {
		return
	}
	var kind string
	switch c.tempKind {
	case "register_activity":
		kind = "activity"
	case "register_workflow":
		kind = "workflow"
	default:
		return
	}
	if edge.Meta == nil {
		edge.Meta = map[string]any{}
	}
	edge.Meta["via"] = "temporal.register"
	edge.Meta["temporal_kind"] = kind
	edge.Meta["temporal_name"] = c.tempName
}

// goTemporalNameFromExpr reduces a single argument expression to the
// trailing identifier that names the activity / workflow. Handles
// string literals (`"MyActivity"` and the Go raw-string variant),
// bare identifiers (`MyActivity`), and selector expressions
// (`pkg.MyActivity`, `a.Method`). Returns "" for any other shape
// (function literals, ternary-style expressions, etc.) — keeps the
// detector high-precision rather than guessing.
func goTemporalNameFromExpr(node *sitter.Node, src []byte) string {
	if node == nil {
		return ""
	}
	switch node.Type() {
	case "interpreted_string_literal", "raw_string_literal":
		text := node.Content(src)
		if len(text) >= 2 && (text[0] == '"' || text[0] == '`') {
			return text[1 : len(text)-1]
		}
		return text
	case "identifier":
		return node.Content(src)
	case "selector_expression":
		if field := node.ChildByFieldName("field"); field != nil {
			return field.Content(src)
		}
	case "unary_expression":
		// `&MyActivity` (rare; mostly seen for struct-method registration)
		if op := node.ChildByFieldName("operand"); op != nil {
			return goTemporalNameFromExpr(op, src)
		}
	}
	return ""
}

// goTemporalEnvDefaultName attempts to resolve a bare-identifier dispatch
// name to the string-literal default of an env-var-with-default
// assignment in the enclosing function. Returns the default and true for
// one of these shapes (anchored on a literal os.Getenv / os.LookupEnv
// read so the value is provably env-sourced):
//
//	name := cmp.Or(os.Getenv("KEY"), "Default")   // any call mixing an
//	                                              // os.Getenv read with a
//	                                              // string-literal arg
//	name := os.Getenv("KEY")
//	if name == "" { name = "Default" }            // (or `name, ok := os.LookupEnv(...)`
//	                                              //  followed by a literal assign)
//
// Intra-procedural and literal-only: only assignments lexically before
// the dispatch call are considered, and anything that isn't an
// os.Getenv-anchored literal default returns "", false. This is a
// deliberately narrow data-flow shortcut, not general constant
// propagation — see the speculative tier the resolver lands it at.
func goTemporalEnvDefaultName(callNode *sitter.Node, name string, src []byte) (string, bool) {
	body := goEnclosingFuncBody(callNode)
	if body == nil {
		return "", false
	}
	limit := callNode.StartByte()
	envDeclSeen := false
	var result string
	var found bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		// Only consider assignments lexically before the dispatch call.
		if (n.Type() == "short_var_declaration" || n.Type() == "assignment_statement") &&
			n.StartByte() < limit && goAssignHasTarget(n, name, src) {
			if rhs := goAssignRHSExpr(n); rhs != nil {
				if rhs.Type() == "call_expression" {
					if goIsEnvRead(rhs, src) {
						envDeclSeen = true
					} else if def, ok := goCallEnvDefaultLiteral(rhs, src); ok {
						result, found = def, true
						return
					}
				} else if envDeclSeen {
					if lit, ok := goStringLiteralValue(rhs, src); ok {
						result, found = lit, true
						return
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
			if found {
				return
			}
		}
	}
	walk(body)
	return result, found
}

// goEnclosingFuncBody walks up from n to the nearest function-like
// ancestor and returns its body block, or nil.
func goEnclosingFuncBody(n *sitter.Node) *sitter.Node {
	for cur := n; cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "function_declaration", "method_declaration", "func_literal":
			return cur.ChildByFieldName("body")
		}
	}
	return nil
}

// goAssignHasTarget reports whether `name` appears among the left-hand
// targets of a short_var_declaration / assignment_statement.
func goAssignHasTarget(assign *sitter.Node, name string, src []byte) bool {
	left := assign.ChildByFieldName("left")
	if left == nil {
		return false
	}
	for i := 0; i < int(left.NamedChildCount()); i++ {
		c := left.NamedChild(i)
		if c != nil && c.Type() == "identifier" && c.Content(src) == name {
			return true
		}
	}
	return false
}

// goAssignRHSExpr returns the first right-hand expression of an
// assignment (the value for a single-target assign, or the lone call for
// a multi-return `a, b := f()`), or nil.
func goAssignRHSExpr(assign *sitter.Node) *sitter.Node {
	right := assign.ChildByFieldName("right")
	if right == nil || right.NamedChildCount() == 0 {
		return nil
	}
	return right.NamedChild(0)
}

// goIsEnvRead reports whether a call_expression is `os.Getenv(...)` or
// `os.LookupEnv(...)`.
func goIsEnvRead(call *sitter.Node, src []byte) bool {
	fn := call.ChildByFieldName("function")
	if fn == nil || fn.Type() != "selector_expression" {
		return false
	}
	op := fn.ChildByFieldName("operand")
	field := fn.ChildByFieldName("field")
	if op == nil || field == nil || op.Content(src) != "os" {
		return false
	}
	switch field.Content(src) {
	case "Getenv", "LookupEnv":
		return true
	}
	return false
}

// goCallEnvDefaultLiteral inspects a call's arguments for the
// env-or-default shape `f(os.Getenv("KEY"), "Default")`: at least one
// argument is an os.Getenv / os.LookupEnv read AND at least one is a
// string literal. Returns the last string-literal argument and true on a
// match.
func goCallEnvDefaultLiteral(call *sitter.Node, src []byte) (string, bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return "", false
	}
	hasEnvRead := false
	lastLiteral := ""
	haveLiteral := false
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type() == "call_expression" && goIsEnvRead(c, src) {
			hasEnvRead = true
			continue
		}
		if lit, ok := goStringLiteralValue(c, src); ok {
			lastLiteral, haveLiteral = lit, true
		}
	}
	if hasEnvRead && haveLiteral {
		return lastLiteral, true
	}
	return "", false
}

// goStringLiteralValue returns the unquoted value of a Go string literal
// node, or ("", false) for any other node type.
func goStringLiteralValue(n *sitter.Node, src []byte) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Type() {
	case "interpreted_string_literal", "raw_string_literal":
		return goTemporalNameFromExpr(n, src), true
	}
	return "", false
}
