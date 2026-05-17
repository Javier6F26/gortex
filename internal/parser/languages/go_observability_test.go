package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestGoObservability_SlogPackageCall(t *testing.T) {
	src := `package foo

import "log/slog"

func Run() {
	slog.Info("user.signup", "id", 42)
	slog.Error("payment.failed")
}
`
	fix := runGoExtract(t, src)

	events := fix.nodesByKind[graph.KindEvent]
	if len(events) != 2 {
		t.Fatalf("expected 2 KindEvent, got %d: %+v", len(events), events)
	}
	gotNames := map[string]bool{}
	for _, e := range events {
		gotNames[e.Name] = true
		if e.ID != "event::log::"+e.Name {
			t.Errorf("event id = %q (expected event::log::<name>)", e.ID)
		}
		if k, _ := e.Meta["event_kind"].(string); k != "log" {
			t.Errorf("event_kind = %q", k)
		}
	}
	if !gotNames["user.signup"] || !gotNames["payment.failed"] {
		t.Errorf("missing event names: %v", gotNames)
	}

	emits := fix.edgesByKind[graph.EdgeEmits]
	// emitGoObservabilityEvents now mirrors every event into a
	// KindString context="log_message" registry node, so each log
	// call emits two edges: one to the KindEvent, one to the
	// KindString. Scope the count by target kind to keep the
	// original assertion's intent (one event-side edge per call).
	eventEmits := 0
	stringEmits := 0
	for _, e := range emits {
		n := fix.nodesByID[e.To]
		if n == nil {
			continue
		}
		switch n.Kind {
		case graph.KindEvent:
			eventEmits++
		case graph.KindString:
			stringEmits++
		}
	}
	if eventEmits != 2 {
		t.Errorf("expected 2 EdgeEmits to KindEvent, got %d", eventEmits)
	}
	if stringEmits != 2 {
		t.Errorf("expected 2 EdgeEmits to KindString log_message, got %d", stringEmits)
	}
	for _, e := range emits {
		if e.From != "pkg/foo.go::Run" {
			t.Errorf("emit from = %q", e.From)
		}
		if m, _ := e.Meta["method"].(string); m != "Info" && m != "Error" {
			t.Errorf("method meta = %q", m)
		}
	}
}

func TestGoObservability_LoggerInstanceCall(t *testing.T) {
	// Generic *Logger.Info(...) call — catches zap, zerolog, logrus,
	// and most internal wrappers without per-provider plumbing.
	src := `package foo

type Logger struct{}

func (l *Logger) Info(msg string, args ...any) {}

func Run(log *Logger) {
	log.Info("auth.failed")
}
`
	fix := runGoExtract(t, src)
	events := fix.nodesByKind[graph.KindEvent]
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Name != "auth.failed" {
		t.Errorf("name = %q", events[0].Name)
	}
}

func TestGoObservability_NonLiteralArgSkipped(t *testing.T) {
	// Dynamic format strings don't produce a stable event name —
	// agents who care about those can grep. The scanner skips them.
	src := `package foo

import "log/slog"

func Run(msg string) {
	slog.Info(msg)
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.nodesByKind[graph.KindEvent]); got != 0 {
		t.Errorf("non-literal arg should not produce KindEvent, got %d", got)
	}
}

func TestGoObservability_DuplicateNameDeduplicates(t *testing.T) {
	// Two emit sites for the same event name should produce one
	// node and two edges, not two nodes.
	src := `package foo

import "log/slog"

func A() { slog.Info("user.signup") }
func B() { slog.Info("user.signup") }
`
	fix := runGoExtract(t, src)
	events := fix.nodesByKind[graph.KindEvent]
	if len(events) != 1 {
		t.Errorf("expected 1 deduped event node, got %d", len(events))
	}
	// Each call site now emits two EdgeEmits — one to the KindEvent,
	// one to the KindString log_message registry shadow. The dedup
	// intent of the test is per-target-kind, so scope by kind.
	eventEmits := 0
	stringEmits := 0
	for _, e := range fix.edgesByKind[graph.EdgeEmits] {
		n := fix.nodesByID[e.To]
		if n == nil {
			continue
		}
		switch n.Kind {
		case graph.KindEvent:
			eventEmits++
		case graph.KindString:
			stringEmits++
		}
	}
	if eventEmits != 2 {
		t.Errorf("expected 2 emit edges to KindEvent (one per call site), got %d", eventEmits)
	}
	if stringEmits != 2 {
		t.Errorf("expected 2 emit edges to KindString log_message (one per call site), got %d", stringEmits)
	}
}

func TestGoObservability_NonLogMethodIgnored(t *testing.T) {
	src := `package foo

type Counter struct{}

func (c *Counter) Add(name string, n int) {}

func Run(c *Counter) {
	c.Add("api.requests", 1)
}
`
	fix := runGoExtract(t, src)
	if got := len(fix.nodesByKind[graph.KindEvent]); got != 0 {
		t.Errorf("non-log method 'Add' should not produce KindEvent, got %d", got)
	}
}
