package resolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zzet/gortex/internal/graph"
)

func celeryTask(g *graph.Graph, id, file, name, registered string) {
	meta := map[string]any{"celery_task": name}
	if registered != "" {
		meta["celery_registered_name"] = registered
	}
	g.AddNode(&graph.Node{ID: id, Kind: graph.KindFunction, Name: name, FilePath: file, Language: "python", Meta: meta})
}

func celeryDispatch(g *graph.Graph, fromID, file, task, registered string) {
	if g.GetNode(fromID) == nil {
		g.AddNode(&graph.Node{ID: fromID, Kind: graph.KindFunction, Name: lastSeg(fromID), FilePath: file, Language: "python"})
	}
	meta := map[string]any{"via": celeryVia, "celery_task": task}
	if registered != "" {
		meta["celery_registered_name"] = registered
	}
	g.AddEdge(&graph.Edge{From: fromID, To: "unresolved::*." + task, Kind: graph.EdgeCalls, FilePath: file, Meta: meta})
}

func synthCeleryEdge(g graph.Store, from, to string) *graph.Edge {
	for e := range g.EdgesByKind(graph.EdgeCalls) {
		if e == nil || e.From != from || e.To != to || e.Meta == nil {
			continue
		}
		if by, _ := e.Meta[MetaSynthesizedBy].(string); by == SynthCelery {
			return e
		}
	}
	return nil
}

func TestResolveCeleryCalls_DelayBindsTaskCrossModule(t *testing.T) {
	g := graph.New()
	celeryTask(g, "tasks.py::send_email", "tasks.py", "send_email", "")
	celeryDispatch(g, "views.py::handle", "views.py", "send_email", "")

	n := ResolveCeleryCalls(g)
	require.Equal(t, 1, n)
	e := synthCeleryEdge(g, "views.py::handle", "tasks.py::send_email")
	require.NotNil(t, e, "view should reach the task across modules")
	assert.Equal(t, ConfidenceTyped, e.Confidence)
	assert.Equal(t, ProvenanceFramework, e.Meta[MetaProvenance])
}

func TestResolveCeleryCalls_SendTaskByRegisteredName(t *testing.T) {
	g := graph.New()
	celeryTask(g, "tasks.py::send_named", "tasks.py", "send_named", "emails.send")
	celeryDispatch(g, "views.py::handle", "views.py", "send", "emails.send")

	require.Equal(t, 1, ResolveCeleryCalls(g))
	assert.NotNil(t, synthCeleryEdge(g, "views.py::handle", "tasks.py::send_named"),
		"send_task('emails.send') binds via the registered name")
}

func TestResolveCeleryCalls_AmbiguousSameNameNotBound(t *testing.T) {
	g := graph.New()
	celeryTask(g, "a.py::process", "a.py", "process", "")
	celeryTask(g, "b.py::process", "b.py", "process", "")
	celeryDispatch(g, "c.py::run", "c.py", "process", "")

	assert.Equal(t, 0, ResolveCeleryCalls(g), "two tasks of the same name in different modules are ambiguous")
}

func TestResolveCeleryCalls_UnknownTaskStaysPlaceholder(t *testing.T) {
	g := graph.New()
	celeryTask(g, "tasks.py::known", "tasks.py", "known", "")
	celeryDispatch(g, "v.py::h", "v.py", "ghost", "")

	assert.Equal(t, 0, ResolveCeleryCalls(g))
	assert.Nil(t, synthCeleryEdge(g, "v.py::h", "tasks.py::known"))
}
