package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

// temporalTestGraph builds the minimal shape ResolveTemporalCalls
// consumes: a workflow function with a temporal.stub call edge, plus
// either a Go register-call edge or a Java @ActivityInterface +
// EdgeImplements chain that names the activity.
type temporalTestGraph struct {
	g graph.Store
}

func newTemporalTestGraph() *temporalTestGraph { return &temporalTestGraph{g: graph.New()} }

// addGoFunc adds a Go function or method node.
func (b *temporalTestGraph) addGoFunc(id, name, filePath, repo string) *graph.Node {
	n := &graph.Node{
		ID: id, Kind: graph.KindFunction, Name: name,
		FilePath: filePath, RepoPrefix: repo, Language: "go",
	}
	b.g.AddNode(n)
	return n
}

// addStubCall adds a Temporal stub-call placeholder edge from caller.
func (b *temporalTestGraph) addStubCall(callerID, kind, name, filePath string) *graph.Edge {
	e := &graph.Edge{
		From: callerID, To: temporalStubPlaceholder(kind, name),
		Kind: graph.EdgeCalls, FilePath: filePath, Line: 10,
		Meta: map[string]any{
			"via":           "temporal.stub",
			"temporal_kind": kind,
			"temporal_name": name,
		},
	}
	b.g.AddEdge(e)
	return e
}

// addStubCallEnvDefault adds a Temporal stub-call edge whose name was
// resolved from an env-var-with-literal-default variable
// (temporal_name_origin=env_default). The resolver must still land it on
// the registered handler but at the speculative tier (the runtime env
// override may differ from the default).
func (b *temporalTestGraph) addStubCallEnvDefault(callerID, kind, name, filePath string) *graph.Edge {
	e := b.addStubCall(callerID, kind, name, filePath)
	e.Meta["temporal_name_origin"] = "env_default"
	return e
}

// addGoRegister adds a Go `worker.RegisterActivity(F)` edge: an
// EdgeCalls edge from the worker-setup function to a placeholder,
// carrying the temporal.register meta the resolver consumes.
func (b *temporalTestGraph) addGoRegister(callerID, kind, name, filePath string) *graph.Edge {
	e := &graph.Edge{
		From: callerID, To: "unresolved::extern::go.temporal.io/sdk/worker::Register" + capitalise(kind),
		Kind: graph.EdgeCalls, FilePath: filePath, Line: 5,
		Meta: map[string]any{
			"via":           "temporal.register",
			"temporal_kind": kind,
			"temporal_name": name,
		},
	}
	b.g.AddEdge(e)
	return e
}

// addJavaInterface adds an interface node tagged with @ActivityInterface
// (or @WorkflowInterface) plus its method nodes (flat, no receiver) and
// the EdgeAnnotated edge to the annotation node the Java extractor
// would emit.
func (b *temporalTestGraph) addJavaInterface(ifaceID, name, filePath string, annoID string, methods ...string) (ifaceNode *graph.Node, methodNodes map[string]*graph.Node) {
	ifaceNode = &graph.Node{
		ID: ifaceID, Kind: graph.KindInterface, Name: name,
		FilePath: filePath, Language: "java",
		StartLine: 10, EndLine: 30,
	}
	b.g.AddNode(ifaceNode)

	annoNode := b.g.GetNode(annoID)
	if annoNode == nil {
		b.g.AddNode(&graph.Node{
			ID: annoID, Kind: graph.KindType, Name: lastSeg(annoID),
			FilePath: filePath, Language: "java",
			Meta: map[string]any{"kind": "annotation", "synthetic": true},
		})
	}
	b.g.AddEdge(&graph.Edge{From: ifaceID, To: annoID, Kind: graph.EdgeAnnotated, FilePath: filePath, Line: 9})

	methodNodes = map[string]*graph.Node{}
	for i, m := range methods {
		mid := filePath + "::" + m
		mn := &graph.Node{
			ID: mid, Kind: graph.KindMethod, Name: m,
			FilePath: filePath, Language: "java",
			StartLine: 11 + i, EndLine: 11 + i,
		}
		b.g.AddNode(mn)
		methodNodes[m] = mn
	}
	return ifaceNode, methodNodes
}

// addJavaImpl adds a Java implementation class with the named methods
// and the EdgeImplements edge from class → interface.
func (b *temporalTestGraph) addJavaImpl(classID, name, filePath, ifaceID string, methods ...string) (classNode *graph.Node, methodNodes map[string]*graph.Node) {
	classNode = &graph.Node{
		ID: classID, Kind: graph.KindType, Name: name,
		FilePath: filePath, Language: "java",
	}
	b.g.AddNode(classNode)
	b.g.AddEdge(&graph.Edge{From: classID, To: ifaceID, Kind: graph.EdgeImplements, FilePath: filePath, Line: 1})

	methodNodes = map[string]*graph.Node{}
	for _, m := range methods {
		mid := filePath + "::" + name + "." + m
		mn := &graph.Node{
			ID: mid, Kind: graph.KindMethod, Name: m,
			FilePath: filePath, Language: "java",
			Meta: map[string]any{"receiver": name},
		}
		b.g.AddNode(mn)
		methodNodes[m] = mn
	}
	return classNode, methodNodes
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// --- Go-side tests --------------------------------------------------

func TestResolveTemporalCalls_GoActivityRegistration(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::OrderWorkflow", "OrderWorkflow", "wf/workflow.go", "svc")
	call := b.addStubCall("wf/workflow.go::OrderWorkflow", "activity", "ChargeCard", "wf/workflow.go")
	activity := b.addGoFunc("wf/activity.go::ChargeCard", "ChargeCard", "wf/activity.go", "svc")
	b.addGoFunc("wf/main.go::setupWorker", "setupWorker", "wf/main.go", "svc")
	b.addGoRegister("wf/main.go::setupWorker", "activity", "ChargeCard", "wf/main.go")

	resolved := ResolveTemporalCalls(b.g)
	assert.Equal(t, 1, resolved)
	assert.Equal(t, activity.ID, call.To, "stub call must land on the registered activity")
	assert.Equal(t, graph.OriginASTResolved, call.Origin)
	assert.Equal(t, 0.9, call.Confidence)
	assert.Equal(t, "EXTRACTED", call.ConfidenceLabel)
	assert.Equal(t, graph.OriginASTResolved, call.Meta["temporal_resolution"])

	assert.Equal(t, "activity", activity.Meta["temporal_role"], "registered activity must carry temporal_role meta")
	assert.Equal(t, "ChargeCard", activity.Meta["temporal_name"])

	require.Len(t, b.g.GetInEdges(activity.ID), 1, "activity must see the inbound call edge")
}

func TestResolveTemporalCalls_EnvDefaultResolvesSpeculative(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::OrderWorkflow", "OrderWorkflow", "wf/workflow.go", "svc")
	call := b.addStubCallEnvDefault("wf/workflow.go::OrderWorkflow", "activity", "ChargeCard", "wf/workflow.go")
	activity := b.addGoFunc("wf/activity.go::ChargeCard", "ChargeCard", "wf/activity.go", "svc")
	b.addGoFunc("wf/main.go::setupWorker", "setupWorker", "wf/main.go", "svc")
	b.addGoRegister("wf/main.go::setupWorker", "activity", "ChargeCard", "wf/main.go")

	resolved := ResolveTemporalCalls(b.g)
	assert.Equal(t, 1, resolved)
	assert.Equal(t, activity.ID, call.To, "env-default stub must still land on the registered activity")
	assert.Equal(t, graph.OriginSpeculative, call.Origin, "env-default resolution must be speculative tier")
	assert.Less(t, call.Confidence, 0.5, "speculative confidence must be below the inferred threshold")
	assert.Equal(t, true, call.Meta[graph.MetaSpeculative], "env-default edge must be hidden-by-default")
}

func TestResolveTemporalCalls_EnvDefaultUnresolvedStaysPlaceholder(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "svc")
	call := b.addStubCallEnvDefault("wf/workflow.go::WF", "activity", "MissingActivity", "wf/workflow.go")

	resolved := ResolveTemporalCalls(b.g)
	assert.Equal(t, 0, resolved)
	assert.Equal(t, temporalStubPlaceholder("activity", "MissingActivity"), call.To)
	_, speculative := call.Meta[graph.MetaSpeculative]
	assert.False(t, speculative, "unresolved env-default edge must not carry the speculative flag")
}

func TestResolveTemporalCalls_GoChildWorkflowRegistration(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("a/parent.go::ParentWorkflow", "ParentWorkflow", "a/parent.go", "svc")
	call := b.addStubCall("a/parent.go::ParentWorkflow", "workflow", "ChildWorkflow", "a/parent.go")
	child := b.addGoFunc("a/child.go::ChildWorkflow", "ChildWorkflow", "a/child.go", "svc")
	b.addGoFunc("a/main.go::setup", "setup", "a/main.go", "svc")
	b.addGoRegister("a/main.go::setup", "workflow", "ChildWorkflow", "a/main.go")

	ResolveTemporalCalls(b.g)
	assert.Equal(t, child.ID, call.To)
	assert.Equal(t, "workflow", child.Meta["temporal_role"])
}

func TestResolveTemporalCalls_GoNoRegistrationStaysPlaceholder(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "svc")
	call := b.addStubCall("wf/workflow.go::WF", "activity", "MissingActivity", "wf/workflow.go")

	resolved := ResolveTemporalCalls(b.g)
	assert.Equal(t, 0, resolved)
	assert.Equal(t, temporalStubPlaceholder("activity", "MissingActivity"), call.To)
	assert.Empty(t, call.Origin)
}

func TestResolveTemporalCalls_GoIdempotent(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "svc")
	call := b.addStubCall("wf/workflow.go::WF", "activity", "Charge", "wf/workflow.go")
	activity := b.addGoFunc("wf/activity.go::Charge", "Charge", "wf/activity.go", "svc")
	b.addGoFunc("wf/main.go::setup", "setup", "wf/main.go", "svc")
	b.addGoRegister("wf/main.go::setup", "activity", "Charge", "wf/main.go")

	first := ResolveTemporalCalls(b.g)
	second := ResolveTemporalCalls(b.g)
	third := ResolveTemporalCalls(b.g)
	assert.Equal(t, 1, first)
	assert.Equal(t, 1, second)
	assert.Equal(t, 1, third)
	assert.Equal(t, activity.ID, call.To)
	require.Len(t, b.g.GetInEdges(activity.ID), 1, "no duplicate inbound edges across re-runs")
}

func TestResolveTemporalCalls_GoReorphanOnHandlerLost(t *testing.T) {
	// First settle the stub call onto a resolved handler, then mutate
	// the call's temporal_name so the next pass can't find a handler
	// for it — the edge must re-orphan to the placeholder and drop
	// its resolution metadata. The same code path runs when the real
	// daemon evicts a register file: the stub-call edge survives the
	// reindex, but the resolver no longer finds a target.
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "svc")
	call := b.addStubCall("wf/workflow.go::WF", "activity", "Charge", "wf/workflow.go")
	b.addGoFunc("wf/activity.go::Charge", "Charge", "wf/activity.go", "svc")
	b.addGoFunc("wf/main.go::setup", "setup", "wf/main.go", "svc")
	b.addGoRegister("wf/main.go::setup", "activity", "Charge", "wf/main.go")

	ResolveTemporalCalls(b.g)
	require.NotEqual(t, temporalStubPlaceholder("activity", "Charge"), call.To)
	require.Equal(t, graph.OriginASTResolved, call.Origin)

	// Re-target the stub call at an activity name nothing registers.
	call.Meta["temporal_name"] = "NoSuchActivity"

	resolved := ResolveTemporalCalls(b.g)
	assert.Equal(t, 0, resolved)
	assert.Equal(t, temporalStubPlaceholder("activity", "NoSuchActivity"), call.To)
	assert.Empty(t, call.Origin)
	_, hasRes := call.Meta["temporal_resolution"]
	assert.False(t, hasRes, "temporal_resolution meta must be cleared on re-orphan")
}

func TestResolveTemporalCalls_GoSameRepoPreference(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("svc/workflow.go::WF", "WF", "svc/workflow.go", "svc")
	call := b.addStubCall("svc/workflow.go::WF", "activity", "Charge", "svc/workflow.go")
	local := b.addGoFunc("svc/activity.go::Charge", "Charge", "svc/activity.go", "svc")
	b.addGoFunc("other/activity.go::Charge", "Charge", "other/activity.go", "other")
	b.addGoFunc("svc/main.go::setup", "setup", "svc/main.go", "svc")
	b.addGoRegister("svc/main.go::setup", "activity", "Charge", "svc/main.go")

	ResolveTemporalCalls(b.g)
	assert.Equal(t, local.ID, call.To, "same-repo activity must win the tie-break")
}

func TestResolveTemporalCalls_GoLocalActivityFlagPreserved(t *testing.T) {
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "svc")
	call := b.addStubCall("wf/workflow.go::WF", "activity", "Lookup", "wf/workflow.go")
	call.Meta["temporal_local"] = true
	b.addGoFunc("wf/activity.go::Lookup", "Lookup", "wf/activity.go", "svc")
	b.addGoFunc("wf/main.go::setup", "setup", "wf/main.go", "svc")
	b.addGoRegister("wf/main.go::setup", "activity", "Lookup", "wf/main.go")

	ResolveTemporalCalls(b.g)
	assert.Equal(t, true, call.Meta["temporal_local"], "local-activity flag must survive the rewrite")
}

func TestResolveTemporalCalls_GoCrossRepoFlowsThroughDetector(t *testing.T) {
	// Workflow in repo "wf", activity in repo "acts", worker setup in
	// repo "wf". After resolution the cross-repo edge layer must
	// materialise a cross_repo_calls parallel edge.
	b := newTemporalTestGraph()
	b.addGoFunc("wf/workflow.go::WF", "WF", "wf/workflow.go", "wf")
	call := b.addStubCall("wf/workflow.go::WF", "activity", "Charge", "wf/workflow.go")
	activity := b.addGoFunc("acts/activity.go::Charge", "Charge", "acts/activity.go", "acts")
	b.addGoFunc("wf/main.go::setup", "setup", "wf/main.go", "wf")
	b.addGoRegister("wf/main.go::setup", "activity", "Charge", "wf/main.go")

	ResolveTemporalCalls(b.g)
	require.Equal(t, activity.ID, call.To)

	emitted := DetectCrossRepoEdges(b.g)
	assert.GreaterOrEqual(t, emitted, 1, "resolved cross-repo Temporal call must materialise a cross_repo_calls edge")
	cr := firstOutEdgeByKind(b.g, "wf/workflow.go::WF", graph.EdgeCrossRepoCalls)
	require.NotNil(t, cr)
	assert.Equal(t, activity.ID, cr.To)
}

// --- Java-side tests ------------------------------------------------

func TestResolveTemporalCalls_JavaActivityInterfacePropagation(t *testing.T) {
	b := newTemporalTestGraph()
	iface, ifaceMethods := b.addJavaInterface(
		"OrderActivities.java::OrderActivities", "OrderActivities", "OrderActivities.java",
		javaActivityIfaceAnnoID, "chargeCard", "shipOrder",
	)
	_, implMethods := b.addJavaImpl(
		"OrderActivitiesImpl.java::OrderActivitiesImpl", "OrderActivitiesImpl",
		"OrderActivitiesImpl.java", iface.ID, "chargeCard", "shipOrder",
	)

	ResolveTemporalCalls(b.g)

	assert.Equal(t, "activity_interface", iface.Meta["temporal_role"])
	assert.Equal(t, "activity", ifaceMethods["chargeCard"].Meta["temporal_role"], "interface methods tagged")
	assert.Equal(t, "activity", ifaceMethods["shipOrder"].Meta["temporal_role"])
	assert.Equal(t, "activity", implMethods["chargeCard"].Meta["temporal_role"], "impl methods tagged via interface chain")
	assert.Equal(t, "activity", implMethods["shipOrder"].Meta["temporal_role"])
}

func TestResolveTemporalCalls_JavaWorkflowInterfacePropagation(t *testing.T) {
	b := newTemporalTestGraph()
	iface, ifaceMethods := b.addJavaInterface(
		"OrderWorkflow.java::OrderWorkflow", "OrderWorkflow", "OrderWorkflow.java",
		javaWorkflowIfaceAnnoID, "processOrder",
	)
	_, implMethods := b.addJavaImpl(
		"OrderWorkflowImpl.java::OrderWorkflowImpl", "OrderWorkflowImpl",
		"OrderWorkflowImpl.java", iface.ID, "processOrder",
	)

	ResolveTemporalCalls(b.g)

	assert.Equal(t, "workflow_interface", iface.Meta["temporal_role"])
	assert.Equal(t, "workflow", ifaceMethods["processOrder"].Meta["temporal_role"])
	assert.Equal(t, "workflow", implMethods["processOrder"].Meta["temporal_role"])
}

func TestResolveTemporalCalls_JavaSignalAndQueryMethods(t *testing.T) {
	b := newTemporalTestGraph()
	// Method-level annotations on a workflow class — no interface-level
	// annotation; signal / query roles still get stamped.
	mid := "Workflow.java::handleSignal"
	method := &graph.Node{
		ID: mid, Kind: graph.KindMethod, Name: "handleSignal",
		FilePath: "Workflow.java", Language: "java",
		StartLine: 20,
	}
	b.g.AddNode(method)
	b.g.AddNode(&graph.Node{
		ID: javaSignalMethodID, Kind: graph.KindType, Name: "SignalMethod",
		FilePath: "Workflow.java", Language: "java",
		Meta: map[string]any{"kind": "annotation", "synthetic": true},
	})
	b.g.AddEdge(&graph.Edge{From: mid, To: javaSignalMethodID, Kind: graph.EdgeAnnotated, FilePath: "Workflow.java", Line: 19})

	qid := "Workflow.java::currentStatus"
	qmethod := &graph.Node{
		ID: qid, Kind: graph.KindMethod, Name: "currentStatus",
		FilePath: "Workflow.java", Language: "java",
		StartLine: 25,
	}
	b.g.AddNode(qmethod)
	b.g.AddNode(&graph.Node{
		ID: javaQueryMethodID, Kind: graph.KindType, Name: "QueryMethod",
		FilePath: "Workflow.java", Language: "java",
		Meta: map[string]any{"kind": "annotation", "synthetic": true},
	})
	b.g.AddEdge(&graph.Edge{From: qid, To: javaQueryMethodID, Kind: graph.EdgeAnnotated, FilePath: "Workflow.java", Line: 24})

	ResolveTemporalCalls(b.g)

	assert.Equal(t, "signal", method.Meta["temporal_role"])
	assert.Equal(t, "query", qmethod.Meta["temporal_role"])
}

func TestResolveTemporalCalls_JavaInterfaceMethodsScopedByLineRange(t *testing.T) {
	// Two interfaces in the same file: only the @ActivityInterface
	// methods get tagged, not the methods on the unrelated interface
	// that follows it.
	b := newTemporalTestGraph()
	b.addJavaInterface(
		"both.java::ActivityIface", "ActivityIface", "both.java",
		javaActivityIfaceAnnoID, "doWork",
	)
	// Inject a second, unrelated interface in the same file with
	// methods OUTSIDE the first interface's line range.
	other := &graph.Node{
		ID: "both.java::OtherIface", Kind: graph.KindInterface, Name: "OtherIface",
		FilePath: "both.java", Language: "java",
		StartLine: 40, EndLine: 60,
	}
	b.g.AddNode(other)
	otherMethod := &graph.Node{
		ID: "both.java::unrelated", Kind: graph.KindMethod, Name: "unrelated",
		FilePath: "both.java", Language: "java",
		StartLine: 45,
	}
	b.g.AddNode(otherMethod)

	ResolveTemporalCalls(b.g)

	_, hasRole := otherMethod.Meta["temporal_role"]
	assert.False(t, hasRole, "unrelated interface's methods must not get tagged")
}

func TestResolveTemporalCalls_RoleStampingIsIdempotent(t *testing.T) {
	b := newTemporalTestGraph()
	_, methods := b.addJavaInterface(
		"Acts.java::Acts", "Acts", "Acts.java",
		javaActivityIfaceAnnoID, "doIt",
	)
	for range 5 {
		ResolveTemporalCalls(b.g)
	}
	assert.Equal(t, "activity", methods["doIt"].Meta["temporal_role"])
}
