package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// Java extractor tests for the Temporal annotation surface. The
// resolver-side tagging (`temporal_role`) is exercised in
// internal/resolver/temporal_calls_test.go; this file pins the
// extractor's contract that every @ActivityInterface /
// @WorkflowInterface / @ActivityMethod / @SignalMethod / @QueryMethod
// annotation is materialised as an EdgeAnnotated edge pointing at a
// well-known synthetic annotation node.

// hasAnnotationEdge reports whether the extraction emitted an
// EdgeAnnotated edge from `fromID` to the canonical annotation node
// for `annoName`.
func hasAnnotationEdge(t *testing.T, edges []*graph.Edge, fromID, annoName string) bool {
	t.Helper()
	want := AnnotationNodeID("java", annoName)
	for _, e := range edges {
		if e.Kind == graph.EdgeAnnotated && e.From == fromID && e.To == want {
			return true
		}
	}
	return false
}

func TestJavaTemporal_ActivityInterfaceAnnotationEdge(t *testing.T) {
	src := []byte(`@ActivityInterface
public interface OrderActivities {
    void chargeCard(String id);
    void shipOrder(String id);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderActivities.java", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	iface := ifaces[0]
	assert.Equal(t, "OrderActivities", iface.Name)

	assert.True(t, hasAnnotationEdge(t, result.Edges, iface.ID, "ActivityInterface"),
		"interface must carry an EdgeAnnotated edge to annotation::java::ActivityInterface")
}

func TestJavaTemporal_WorkflowInterfaceAnnotationEdge(t *testing.T) {
	src := []byte(`@WorkflowInterface
public interface OrderWorkflow {
    void processOrder(String id);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderWorkflow.java", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.True(t, hasAnnotationEdge(t, result.Edges, ifaces[0].ID, "WorkflowInterface"))
}

func TestJavaTemporal_MethodLevelAnnotationsCarried(t *testing.T) {
	src := []byte(`public class OrderWorkflowImpl {
    @SignalMethod
    public void cancel() {}

    @QueryMethod
    public String status() { return null; }

    @UpdateMethod
    public void retry() {}
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderWorkflowImpl.java", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	byName := map[string]*graph.Node{}
	for _, m := range methods {
		byName[m.Name] = m
	}
	require.Contains(t, byName, "cancel")
	require.Contains(t, byName, "status")
	require.Contains(t, byName, "retry")

	assert.True(t, hasAnnotationEdge(t, result.Edges, byName["cancel"].ID, "SignalMethod"))
	assert.True(t, hasAnnotationEdge(t, result.Edges, byName["status"].ID, "QueryMethod"))
	assert.True(t, hasAnnotationEdge(t, result.Edges, byName["retry"].ID, "UpdateMethod"))
}

func TestJavaTemporal_ActivityMethodAnnotation(t *testing.T) {
	src := []byte(`@ActivityInterface
public interface OrderActivities {
    @ActivityMethod(name = "ChargeCard")
    void chargeCard(String id);
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderActivities.java", src)
	require.NoError(t, err)

	// The method-level @ActivityMethod annotation must travel
	// alongside the interface-level @ActivityInterface annotation —
	// both edges are needed by the resolver, neither replaces the
	// other.
	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.Len(t, methods, 1)
	method := methods[0]
	assert.True(t, hasAnnotationEdge(t, result.Edges, method.ID, "ActivityMethod"),
		"method-level @ActivityMethod must emit its own EdgeAnnotated edge")
}

// temporalStartEdge returns the via=temporal.start edge originating at
// fromID, or nil.
func temporalStartEdge(edges []*graph.Edge, fromID string) *graph.Edge {
	for _, e := range edges {
		if e.From == fromID && e.Meta != nil && e.Meta["via"] == "temporal.start" {
			return e
		}
	}
	return nil
}

func TestJavaTemporal_NewWorkflowStubStart(t *testing.T) {
	src := []byte(`public class OrderService {
    public void start(WorkflowClient client) {
        OrderWorkflow wf = client.newWorkflowStub(OrderWorkflow.class, options);
        wf.processOrder("id");
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderService.java", src)
	require.NoError(t, err)

	var startMethod string
	for _, n := range result.Nodes {
		if n.Name == "start" {
			startMethod = n.ID
		}
	}
	require.NotEmpty(t, startMethod, "start method must be indexed")

	edge := temporalStartEdge(result.Edges, startMethod)
	require.NotNil(t, edge, "newWorkflowStub must emit a via=temporal.start edge")
	assert.Equal(t, "workflow", edge.Meta["temporal_kind"])
	assert.Equal(t, "OrderWorkflow", edge.Meta["temporal_name"],
		"the class literal's simple name is the canonical workflow type")
}

func TestJavaTemporal_NewUntypedWorkflowStubStart(t *testing.T) {
	src := []byte(`public class OrderService {
    public void start(WorkflowClient client) {
        client.newUntypedWorkflowStub("OrderWorkflow");
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("OrderService.java", src)
	require.NoError(t, err)

	var startMethod string
	for _, n := range result.Nodes {
		if n.Name == "start" {
			startMethod = n.ID
		}
	}
	require.NotEmpty(t, startMethod)
	edge := temporalStartEdge(result.Edges, startMethod)
	require.NotNil(t, edge)
	assert.Equal(t, "OrderWorkflow", edge.Meta["temporal_name"])
}

func temporalEdgeByViaFrom(edges []*graph.Edge, fromID, via string) *graph.Edge {
	for _, e := range edges {
		if e.From == fromID && e.Meta != nil && e.Meta["via"] == via {
			return e
		}
	}
	return nil
}

func TestJavaTemporal_UntypedStubSignalSend(t *testing.T) {
	src := []byte(`public class Canceller {
    public void cancel(WorkflowClient client) {
        WorkflowStub stub = client.newUntypedWorkflowStub("OrderWorkflow");
        stub.signal("cancel-request", null);
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Canceller.java", src)
	require.NoError(t, err)

	var fromID string
	for _, n := range result.Nodes {
		if n.Name == "cancel" {
			fromID = n.ID
		}
	}
	require.NotEmpty(t, fromID)
	edge := temporalEdgeByViaFrom(result.Edges, fromID, "temporal.signal-send")
	require.NotNil(t, edge, "stub.signal on a WorkflowStub must emit a signal-send edge")
	assert.Equal(t, "signal", edge.Meta["temporal_kind"])
	assert.Equal(t, "cancel-request", edge.Meta["temporal_name"])
}

func TestJavaTemporal_SignalOnNonStubIgnored(t *testing.T) {
	// `signal` on a receiver that is NOT a WorkflowStub must not be
	// detected — the type gate keeps the common method name precise.
	src := []byte(`public class Light {
    public void flip(Lamp lamp) {
        lamp.signal("on");
    }
}
`)
	e := NewJavaExtractor()
	result, err := e.Extract("Light.java", src)
	require.NoError(t, err)

	var fromID string
	for _, n := range result.Nodes {
		if n.Name == "flip" {
			fromID = n.ID
		}
	}
	require.NotEmpty(t, fromID)
	assert.Nil(t, temporalEdgeByViaFrom(result.Edges, fromID, "temporal.signal-send"),
		"signal on a non-WorkflowStub receiver must not be detected")
}
