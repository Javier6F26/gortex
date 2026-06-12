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

// --- Dispatch name from an env-var-with-literal-default variable -----
//
// When the activity / workflow name is a local variable read from an
// env var with a literal fallback, resolve to the literal default and
// flag the stub edge `temporal_name_origin=env_default` so the resolver
// lands it at the speculative tier (the runtime env override may differ
// from the default). Anchored on a literal os.Getenv / os.LookupEnv read
// so the value is provably env-sourced — no general data-flow guessing.

func TestGoTemporal_ExecuteActivity_EnvDefault_CmpOr(t *testing.T) {
	fix := runGoExtract(t, `package wf

import (
	"cmp"
	"os"
	"go.temporal.io/sdk/workflow"
)

func WF(ctx workflow.Context) {
	actName := cmp.Or(os.Getenv("CHARGE_ACTIVITY"), "ChargeCard")
	workflow.ExecuteActivity(ctx, actName, 1)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	e := edges[0]
	assert.Equal(t, "unresolved::temporal::activity::ChargeCard", e.To,
		"name must resolve to the literal default, not the variable identifier")
	assert.Equal(t, "ChargeCard", e.Meta["temporal_name"])
	assert.Equal(t, "env_default", e.Meta["temporal_name_origin"])
}

func TestGoTemporal_ExecuteActivity_EnvDefault_IfEmpty(t *testing.T) {
	fix := runGoExtract(t, `package wf

import (
	"os"
	"go.temporal.io/sdk/workflow"
)

func WF(ctx workflow.Context) {
	name := os.Getenv("CHARGE_ACTIVITY")
	if name == "" {
		name = "ChargeCard"
	}
	workflow.ExecuteActivity(ctx, name, 1)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved::temporal::activity::ChargeCard", edges[0].To)
	assert.Equal(t, "ChargeCard", edges[0].Meta["temporal_name"])
	assert.Equal(t, "env_default", edges[0].Meta["temporal_name_origin"])
}

func TestGoTemporal_ExecuteActivity_PlainVarNotEnvDefault(t *testing.T) {
	// A variable NOT sourced from an env read keeps the existing
	// behaviour (trailing identifier as the name) and carries no
	// env_default flag — we don't guess at arbitrary variables.
	fix := runGoExtract(t, `package wf

import "go.temporal.io/sdk/workflow"

func WF(ctx workflow.Context, picked string) {
	actName := picked
	workflow.ExecuteActivity(ctx, actName, 1)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "actName", edges[0].Meta["temporal_name"])
	_, flagged := edges[0].Meta["temporal_name_origin"]
	assert.False(t, flagged, "plain variable must not be flagged env_default")
}

func TestGoTemporal_ExecuteActivity_EnvReadNoLiteralDefault(t *testing.T) {
	// os.Getenv with no literal fallback can't be pinned to a name —
	// keep the variable identifier, no env_default flag.
	fix := runGoExtract(t, `package wf

import (
	"os"
	"go.temporal.io/sdk/workflow"
)

func WF(ctx workflow.Context) {
	name := os.Getenv("CHARGE_ACTIVITY")
	workflow.ExecuteActivity(ctx, name, 1)
}
`)
	edges := temporalEdgesByVia(fix, "temporal.stub")
	require.Len(t, edges, 1)
	assert.Equal(t, "name", edges[0].Meta["temporal_name"])
	_, flagged := edges[0].Meta["temporal_name_origin"]
	assert.False(t, flagged)
}
