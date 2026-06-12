package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// temporalEdgesByVia returns every EdgeCalls edge tagged with the given
// `via` value (e.g. "temporal.stub" or "temporal.register").
func temporalEdgesByVia(fix *extractedFixture, via string) []*graph.Edge {
	var found []*graph.Edge
	for _, e := range fix.edgesByKind[graph.EdgeCalls] {
		if e.Meta != nil && e.Meta["via"] == via {
			found = append(found, e)
		}
	}
	return found
}

func TestGoTemporal_ExecuteActivity_IdentifierName(t *testing.T) {
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func OrderWorkflow(ctx workflow.Context, id string) error {
	workflow.ExecuteActivity(ctx, ChargeCard, id)
	return nil
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "unresolved::temporal::activity::ChargeCard", e.To)
	assert.Equal(t, "activity", e.Meta["temporal_kind"])
	assert.Equal(t, "ChargeCard", e.Meta["temporal_name"])
	_, isLocal := e.Meta["temporal_local"]
	assert.False(t, isLocal, "ExecuteActivity must not flag temporal_local")
}

func TestGoTemporal_ExecuteActivity_StringLiteralName(t *testing.T) {
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func WF(ctx workflow.Context) {
	workflow.ExecuteActivity(ctx, "RemoteActivity", nil)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved::temporal::activity::RemoteActivity", edges[0].To)
	assert.Equal(t, "RemoteActivity", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_ExecuteActivity_SelectorName(t *testing.T) {
	// `workflow.ExecuteActivity(ctx, pkg.Charge, ...)` → name is "Charge"
	// (the trailing identifier of the selector).
	fix := runGoExtract(t, `package wf

import (
	"go.temporal.io/sdk/workflow"
	"example.com/activities"
)

func WF(ctx workflow.Context) {
	workflow.ExecuteActivity(ctx, activities.Charge, 1)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved::temporal::activity::Charge", edges[0].To)
}

func TestGoTemporal_ExecuteLocalActivity_FlagsTemporalLocal(t *testing.T) {
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func WF(ctx workflow.Context) {
	workflow.ExecuteLocalActivity(ctx, Lookup, "k")
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "activity", e.Meta["temporal_kind"])
	assert.Equal(t, true, e.Meta["temporal_local"], "ExecuteLocalActivity must flag temporal_local")
}

func TestGoTemporal_ExecuteChildWorkflow_KindIsWorkflow(t *testing.T) {
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func Parent(ctx workflow.Context) {
	workflow.ExecuteChildWorkflow(ctx, ChildWorkflow, 42)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved::temporal::workflow::ChildWorkflow", edges[0].To)
	assert.Equal(t, "workflow", edges[0].Meta["temporal_kind"])
}

func TestGoTemporal_RegisterActivity(t *testing.T) {
	fix := runGoExtract(t, `package main

func setup(w Worker) {
	w.RegisterActivity(ChargeCard)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.register")
	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "activity", e.Meta["temporal_kind"])
	assert.Equal(t, "ChargeCard", e.Meta["temporal_name"])
}

func TestGoTemporal_RegisterActivityWithOptions(t *testing.T) {
	fix := runGoExtract(t, `package main

import "go.temporal.io/sdk/activity"

func setup(w Worker) {
	w.RegisterActivityWithOptions(ChargeCard, activity.RegisterOptions{Name: "Charge"})
}
`)
	edges := temporalEdgesByVia(fix, "temporal.register")
	require.Len(t, edges, 1)
	assert.Equal(t, "activity", edges[0].Meta["temporal_kind"])
	assert.Equal(t, "ChargeCard", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_RegisterWorkflow(t *testing.T) {
	fix := runGoExtract(t, `package main

func setup(w Worker) {
	w.RegisterWorkflow(OrderWorkflow)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.register")
	require.Len(t, edges, 1)
	assert.Equal(t, "workflow", edges[0].Meta["temporal_kind"])
	assert.Equal(t, "OrderWorkflow", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_OtherWorkflowMethodNotStubbed(t *testing.T) {
	// `workflow.Sleep` / `workflow.Now` / etc. must NOT be stamped as
	// temporal.stub — only the four explicit dispatch helpers are.
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func WF(ctx workflow.Context) {
	workflow.Sleep(ctx, 5)
	workflow.Now(ctx)
}
`)
	assert.Empty(t, temporalEdgesByVia(fix, "temporal.stub"),
		"only ExecuteActivity / ExecuteLocalActivity / ExecuteChildWorkflow should be stub-tagged")
}

func TestGoTemporal_AliasedImportNotDetected(t *testing.T) {
	// We require the receiver text to be exactly "workflow" — aliased
	// imports (intentionally) miss; this test pins that contract so a
	// future relaxation is a conscious decision.
	fix := runGoExtract(t, `package wf

import wf "go.temporal.io/sdk/workflow"

func WF(ctx wf.Context) {
	wf.ExecuteActivity(ctx, Charge, 1)
}
`)
	assert.Empty(t, temporalEdgesByVia(fix, "temporal.stub"))
}

func TestGoTemporal_StubAndRegisterCoexistInSameFile(t *testing.T) {
	fix := runGoExtract(t, `package main

import "go.temporal.io/sdk/workflow"

func Charge() error { return nil }

func WF(ctx workflow.Context) {
	workflow.ExecuteActivity(ctx, Charge, 1)
}

func setup(w Worker) {
	w.RegisterActivity(Charge)
	w.RegisterWorkflow(WF)
}
`)
	stubs := temporalEdgesByVia(fix, "temporal.stub")
	registers := temporalEdgesByVia(fix, "temporal.register")
	require.Len(t, stubs, 1)
	require.Len(t, registers, 2)
}

// --- Outbound signal sends / query calls ----------------------------
//
// A workflow (or a service holding a Temporal client) can signal or
// query an ALREADY-RUNNING workflow by name. These are the consumer
// side of the signal/query namespaces — distinct from the in-workflow
// handler declarations. We surface them as EdgeCalls edges tagged
// `via=temporal.signal-send` / `temporal.query-call` carrying the
// signal/query name (the 4th positional argument, a string literal).
//
// APIs (the name is always the 4th positional arg, after ctx +
// workflowID + runID):
//
//	workflow.SignalExternalWorkflow(ctx, wid, rid, "name", arg)   // workflow -> workflow
//	client.SignalWorkflow(ctx, wid, rid, "name", arg)            // service  -> workflow
//	client.QueryWorkflow(ctx, wid, rid, "name", args...)         // service  -> workflow
//
// (Note: there is no workflow.QueryWorkflow — querying is a client-side
// operation; and SignalExternalWorkflow already returns a Future, so
// there is no ...Async variant.)

func TestGoTemporal_SignalExternalWorkflow(t *testing.T) {
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func Orchestrator(ctx workflow.Context) error {
	return workflow.SignalExternalWorkflow(ctx, "order-123", "", "cancel-request", nil).Get(ctx, nil)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.signal-send")
	require.Len(t, edges, 1)
	assert.Equal(t, "signal", edges[0].Meta["temporal_kind"])
	assert.Equal(t, "cancel-request", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_ClientSignalWorkflow(t *testing.T) {
	// Receiver is an arbitrary client variable, so detection is by
	// method name (like the Register* helpers), gated on a string-literal
	// name in the 4th position.
	fix := runGoExtract(t, `package svc

func Cancel(c Client) error {
	return c.SignalWorkflow(ctx, "order-123", "", "cancel-request", nil)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.signal-send")
	require.Len(t, edges, 1)
	assert.Equal(t, "signal", edges[0].Meta["temporal_kind"])
	assert.Equal(t, "cancel-request", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_ClientQueryWorkflow(t *testing.T) {
	fix := runGoExtract(t, `package svc

func Status(c Client) {
	c.QueryWorkflow(ctx, "order-123", "", "get-status")
}
`)
	edges := temporalEdgesByVia(fix, "temporal.query-call")
	require.Len(t, edges, 1)
	assert.Equal(t, "query", edges[0].Meta["temporal_kind"])
	assert.Equal(t, "get-status", edges[0].Meta["temporal_name"])
}

func TestGoTemporal_OutboundNonLiteralNameUndetected(t *testing.T) {
	// Signal/query names are matched by string at runtime; a non-literal
	// name can't be pinned, so no outbound edge is emitted.
	fix := runGoExtract(t, `package svc

func Cancel(c Client, name string) error {
	return c.SignalWorkflow(ctx, "order-123", "", name, nil)
}
`)
	assert.Empty(t, temporalEdgesByVia(fix, "temporal.signal-send"))
}

func TestGoTemporal_SignalExternalAliasedNotDetected(t *testing.T) {
	// SignalExternalWorkflow is gated on the canonical "workflow"
	// receiver (it is a workflow-package function); an aliased import
	// is intentionally missed, consistent with the dispatch detector.
	fix := runGoExtract(t, `package wf

import wf "go.temporal.io/sdk/workflow"

func Orchestrator(ctx wf.Context) error {
	return wf.SignalExternalWorkflow(ctx, "order-123", "", "cancel-request", nil).Get(ctx, nil)
}
`)
	assert.Empty(t, temporalEdgesByVia(fix, "temporal.signal-send"))
}
