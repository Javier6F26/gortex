package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func celeryTaskNode(nodes []*graph.Node, name string) *graph.Node {
	for _, n := range nodes {
		if n.Meta == nil {
			continue
		}
		if t, _ := n.Meta["celery_task"].(string); t == name {
			return n
		}
	}
	return nil
}

func celeryPlaceholder(edges []*graph.Edge, task string) *graph.Edge {
	for _, e := range edges {
		if e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "celery-dispatch" {
			continue
		}
		if ct, _ := e.Meta["celery_task"].(string); ct == task {
			return e
		}
	}
	return nil
}

func TestCelery_TagsTaskFunctions(t *testing.T) {
	src := `from celery import shared_task

@shared_task
def send_email(uid):
    return uid

@app.task(name="emails.send")
def send_named(uid):
    return uid

def not_a_task():
    return 1
`
	res, err := NewPythonExtractor().Extract("tasks.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if n := celeryTaskNode(res.Nodes, "send_email"); n == nil {
		t.Errorf("@shared_task send_email not tagged")
	}
	named := celeryTaskNode(res.Nodes, "send_named")
	if named == nil {
		t.Fatalf("@app.task send_named not tagged")
	}
	if reg, _ := named.Meta["celery_registered_name"].(string); reg != "emails.send" {
		t.Errorf("registered name = %q (want emails.send)", reg)
	}
	if celeryTaskNode(res.Nodes, "not_a_task") != nil {
		t.Errorf("undecorated function must not be tagged a task")
	}
}

func TestCelery_DispatchPlaceholders(t *testing.T) {
	src := `from tasks import send_email

def handle(user):
    send_email.delay(user.id)
    send_email.apply_async((user.id,))
    current_app.send_task("emails.send")
`
	res, err := NewPythonExtractor().Extract("views.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	d := celeryPlaceholder(res.Edges, "send_email")
	if d == nil {
		t.Fatalf("no placeholder for send_email.delay()")
	}
	if d.From != "views.py::handle" {
		t.Errorf("placeholder From = %q (want views.py::handle)", d.From)
	}
	send := celeryPlaceholder(res.Edges, "send")
	if send == nil {
		t.Fatalf("no placeholder for send_task('emails.send')")
	}
	if reg, _ := send.Meta["celery_registered_name"].(string); reg != "emails.send" {
		t.Errorf("send_task registered name = %q (want emails.send)", reg)
	}
}

func TestCelery_PlainCallNoPlaceholder(t *testing.T) {
	// A plain function call (not .delay/.apply_async/.s/send_task) is not a
	// Celery dispatch.
	src := `def handle():
    send_email(1)
`
	res, err := NewPythonExtractor().Extract("v.py", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if celeryPlaceholder(res.Edges, "send_email") != nil {
		t.Errorf("a direct call must not produce a celery placeholder")
	}
}
