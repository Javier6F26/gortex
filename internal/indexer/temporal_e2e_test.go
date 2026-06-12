package indexer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// TestTemporalE2E_GoWorkflowToActivity exercises the full pipeline —
// parser detection → graph emission → resolver rewriting — on a tiny
// Go fixture that registers an activity + a workflow and dispatches
// the activity from the workflow body. After indexing, the
// EdgeCalls placeholder must point at the real activity function
// node and both the activity and the workflow must carry
// `temporal_role` Meta tags.
func TestTemporalE2E_GoWorkflowToActivity(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "workflow.go"), `package wf

import "go.temporal.io/sdk/workflow"

func OrderWorkflow(ctx workflow.Context, id string) error {
	return workflow.ExecuteActivity(ctx, ChargeCard, id).Get(ctx, nil)
}
`)
	writeFile(t, filepath.Join(dir, "activity.go"), `package wf

import "context"

func ChargeCard(ctx context.Context, id string) error {
	return nil
}
`)
	writeFile(t, filepath.Join(dir, "main.go"), `package wf

func setupWorker(w Worker) {
	w.RegisterWorkflow(OrderWorkflow)
	w.RegisterActivity(ChargeCard)
}
`)

	g := graph.New()
	idx := newTestIndexer(g)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	// The activity function node was discovered via the
	// `worker.RegisterActivity` edge and stamped temporal_role.
	activityNodes := g.FindNodesByName("ChargeCard")
	require.Len(t, activityNodes, 1)
	activity := activityNodes[0]
	assert.Equal(t, "activity", activity.Meta["temporal_role"],
		"registered activity must carry temporal_role meta")
	assert.Equal(t, "ChargeCard", activity.Meta["temporal_name"])

	// The workflow was stamped too.
	workflowNodes := g.FindNodesByName("OrderWorkflow")
	require.Len(t, workflowNodes, 1)
	wf := workflowNodes[0]
	assert.Equal(t, "workflow", wf.Meta["temporal_role"])

	// The workflow.ExecuteActivity call edge was rewritten from the
	// placeholder to the real activity function.
	var stubCall *graph.Edge
	for _, e := range g.GetOutEdges(wf.ID) {
		if e == nil || e.Meta == nil {
			continue
		}
		if e.Meta["via"] == "temporal.stub" {
			stubCall = e
			break
		}
	}
	require.NotNil(t, stubCall, "workflow must have an outbound temporal.stub edge")
	assert.Equal(t, activity.ID, stubCall.To,
		"stub-call edge must land on the registered activity, not the placeholder")
	assert.Equal(t, graph.OriginASTResolved, stubCall.Origin)
}

// TestTemporalE2E_GoChildWorkflow exercises the same pipeline on a
// child-workflow dispatch — a different temporal_kind, same resolver
// path.
func TestTemporalE2E_GoChildWorkflow(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "parent.go"), `package wf

import "go.temporal.io/sdk/workflow"

func ParentWorkflow(ctx workflow.Context) error {
	return workflow.ExecuteChildWorkflow(ctx, ChildWorkflow, 42).Get(ctx, nil)
}
`)
	writeFile(t, filepath.Join(dir, "child.go"), `package wf

import "go.temporal.io/sdk/workflow"

func ChildWorkflow(ctx workflow.Context, n int) error {
	return nil
}
`)
	writeFile(t, filepath.Join(dir, "main.go"), `package wf

func setup(w Worker) {
	w.RegisterWorkflow(ParentWorkflow)
	w.RegisterWorkflow(ChildWorkflow)
}
`)

	g := graph.New()
	idx := newTestIndexer(g)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	parent := g.FindNodesByName("ParentWorkflow")[0]
	child := g.FindNodesByName("ChildWorkflow")[0]
	assert.Equal(t, "workflow", parent.Meta["temporal_role"])
	assert.Equal(t, "workflow", child.Meta["temporal_role"])

	var stubCall *graph.Edge
	for _, e := range g.GetOutEdges(parent.ID) {
		if e != nil && e.Meta != nil && e.Meta["via"] == "temporal.stub" {
			stubCall = e
			break
		}
	}
	require.NotNil(t, stubCall, "parent workflow must have an outbound temporal.stub edge")
	assert.Equal(t, child.ID, stubCall.To)
	assert.Equal(t, "workflow", stubCall.Meta["temporal_kind"])
}

// TestTemporalE2E_GoEnvDefaultActivity exercises the env-var-with-literal
// -default dispatch name: the workflow names its activity through a
// variable read from os.Getenv with a literal fallback. The pipeline must
// land the call on the default activity but at the speculative tier.
func TestTemporalE2E_GoEnvDefaultActivity(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "workflow.go"), `package wf

import (
	"cmp"
	"os"

	"go.temporal.io/sdk/workflow"
)

func OrderWorkflow(ctx workflow.Context, id string) error {
	actName := cmp.Or(os.Getenv("CHARGE_ACTIVITY"), "ChargeCard")
	return workflow.ExecuteActivity(ctx, actName, id).Get(ctx, nil)
}
`)
	writeFile(t, filepath.Join(dir, "activity.go"), `package wf

import "context"

func ChargeCard(ctx context.Context, id string) error {
	return nil
}
`)
	writeFile(t, filepath.Join(dir, "main.go"), `package wf

func setupWorker(w Worker) {
	w.RegisterWorkflow(OrderWorkflow)
	w.RegisterActivity(ChargeCard)
}
`)

	g := graph.New()
	idx := newTestIndexer(g)
	_, err := idx.Index(dir)
	require.NoError(t, err)

	wf := g.FindNodesByName("OrderWorkflow")[0]
	activity := g.FindNodesByName("ChargeCard")[0]

	var stubCall *graph.Edge
	for _, e := range g.GetOutEdges(wf.ID) {
		if e != nil && e.Meta != nil && e.Meta["via"] == "temporal.stub" {
			stubCall = e
			break
		}
	}
	require.NotNil(t, stubCall, "workflow must have an outbound temporal.stub edge")
	assert.Equal(t, activity.ID, stubCall.To,
		"env-default dispatch must land on the default activity")
	assert.Equal(t, "env_default", stubCall.Meta["temporal_name_origin"])
	assert.Equal(t, graph.OriginSpeculative, stubCall.Origin,
		"env-default resolution must be speculative")
	assert.Equal(t, true, stubCall.Meta[graph.MetaSpeculative],
		"env-default edge must be hidden-by-default")
}
