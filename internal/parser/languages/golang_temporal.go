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

// goTemporalSignalQueryOutKind reports whether (receiver, method) names
// an OUTBOUND signal-send or query-call against an already-running
// workflow and, if so, returns the kind ("signal" / "query") plus the
// 1-based position of the signal/query-name argument.
//
//	workflow.SignalExternalWorkflow(ctx, wid, rid, "name", arg)  // wf -> wf
//	client.SignalWorkflow(ctx, wid, rid, "name", arg)           // svc -> wf
//	client.QueryWorkflow(ctx, wid, rid, "name", args...)        // svc -> wf
//
// SignalExternalWorkflow is gated on the canonical "workflow" receiver
// (it is a workflow-package function). SignalWorkflow / QueryWorkflow
// live on the client and are called on an arbitrary client variable, so
// — like the Register* helpers — they are matched by method name alone;
// the string-literal name gate below keeps that high-precision. There is
// deliberately no workflow.QueryWorkflow (querying is client-side) and no
// SignalExternalWorkflowAsync (SignalExternalWorkflow returns a Future).
func goTemporalSignalQueryOutKind(receiver, method string) (kind string, namePos int, ok bool) {
	switch method {
	case "SignalExternalWorkflow":
		if receiver == "workflow" {
			return "signal", 4, true
		}
	case "SignalWorkflow":
		return "signal", 4, true
	case "QueryWorkflow":
		return "query", 4, true
	}
	return "", 0, false
}

// goTemporalDispatchName extracts the activity (or child-workflow)
// name from a `workflow.ExecuteActivity(ctx, X, args...)` call. X is
// the second positional argument and is either:
//
//   - a string literal:                    "MyActivity"
//   - a bare identifier:                   MyActivity
//   - a selector expression:               pkg.MyActivity, recv.Method
//
// In every case we return the trailing identifier — that's the name
// the worker registers under (Temporal Go SDK convention: the bare
// function name unless `RegisterActivityWithOptions` overrides it).
// Returns "" when the second argument is missing, an expression we
// can't reduce to a name (e.g. a function literal), or when the call
// has fewer than two positional arguments.
func goTemporalDispatchName(callNode *sitter.Node, src []byte) string {
	if callNode == nil || callNode.Type() != "call_expression" {
		return ""
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	count := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		count++
		if count == 2 {
			return goTemporalNameFromExpr(c, src)
		}
	}
	return ""
}

// goTemporalNthStringLiteralArg returns the unquoted value of the n-th
// (1-based) positional argument of a call when that argument is a string
// literal, else "". Used to extract the signal/query name from an
// outbound send/call — names are matched by string at runtime, so only a
// literal can be pinned here (a variable / constant is left undetected,
// keeping the detector high-precision).
func goTemporalNthStringLiteralArg(callNode *sitter.Node, n int, src []byte) string {
	if callNode == nil || callNode.Type() != "call_expression" {
		return ""
	}
	args := callNode.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	count := 0
	for i := 0; i < int(args.NamedChildCount()); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		count++
		if count == n {
			switch c.Type() {
			case "interpreted_string_literal", "raw_string_literal":
				return goTemporalNameFromExpr(c, src)
			}
			return ""
		}
	}
	return ""
}

// goTemporalRegisterName extracts the registered function name from a
// `worker.RegisterActivity(F)` / `worker.RegisterWorkflow(F)` call —
// the first positional argument, which is the function reference.
// Same expression shapes as goTemporalDispatchName.
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

// applyGoTemporalSignalQueryMeta stamps the outbound signal-send /
// query-call meta onto an EdgeCalls edge derived from
// `SignalExternalWorkflow` / `SignalWorkflow` / `QueryWorkflow`:
// `via=temporal.signal-send` or `temporal.query-call`, plus
// `temporal_kind` (signal / query) and `temporal_name` (the literal
// signal/query name). No-op when c.tempOutKind / c.tempName are unset.
//
// These are the consumer side of the signal/query namespaces; the
// provider side is the in-workflow handler (GetSignalChannel /
// SetQueryHandler), tagged via=temporal.handler.
func applyGoTemporalSignalQueryMeta(edge *graph.Edge, c goDeferredCall) {
	if edge == nil || c.tempOutKind == "" || c.tempName == "" {
		return
	}
	var via string
	switch c.tempOutKind {
	case "signal":
		via = "temporal.signal-send"
	case "query":
		via = "temporal.query-call"
	default:
		return
	}
	if edge.Meta == nil {
		edge.Meta = map[string]any{}
	}
	edge.Meta["via"] = via
	edge.Meta["temporal_kind"] = c.tempOutKind
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
