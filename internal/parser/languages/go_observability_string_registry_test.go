package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// Every log call mirrors into a KindString context="log_message"
// registry node and an EdgeEmits from the caller, alongside the
// pre-existing KindEvent emission. Powers the log_events analyzer.
func TestGoObservability_EmitsLogMessageKindString(t *testing.T) {
	src := `package foo

import "log/slog"

func Run() {
	slog.Error("payment.failed")
}
`
	fix := runGoExtract(t, src)

	var logStrings []*graph.Node
	for _, n := range fix.nodesByKind[graph.KindString] {
		if ctx, _ := n.Meta["context"].(string); ctx == "log_message" {
			logStrings = append(logStrings, n)
		}
	}
	if len(logStrings) != 1 {
		t.Fatalf("expected 1 KindString context=log_message, got %d", len(logStrings))
	}
	n := logStrings[0]
	if n.Name != "payment.failed" {
		t.Errorf("KindString name = %q", n.Name)
	}
	if level, _ := n.Meta["level"].(string); level != "log" {
		t.Errorf("level meta = %q, want log", level)
	}
	if eventID, _ := n.Meta["event"].(string); eventID != "event::log::payment.failed" {
		t.Errorf("event meta = %q", eventID)
	}

	// EdgeEmits from caller to the KindString carries the matched
	// method and severity.
	emits := 0
	for _, e := range fix.edgesByKind[graph.EdgeEmits] {
		if e.To != n.ID {
			continue
		}
		emits++
		if e.From != "pkg/foo.go::Run" {
			t.Errorf("emit From = %q", e.From)
		}
		if m, _ := e.Meta["method"].(string); m != "Error" {
			t.Errorf("method meta = %q", m)
		}
		if level, _ := e.Meta["level"].(string); level != "log" {
			t.Errorf("level meta = %q", level)
		}
	}
	if emits != 1 {
		t.Errorf("expected 1 EdgeEmits to KindString log_message, got %d", emits)
	}
}

func TestGoObservability_LogMessageDedupAcrossCallers(t *testing.T) {
	src := `package foo

import "log/slog"

func A() { slog.Info("user.signup") }
func B() { slog.Info("user.signup") }
`
	fix := runGoExtract(t, src)
	var logStrings []*graph.Node
	for _, n := range fix.nodesByKind[graph.KindString] {
		if ctx, _ := n.Meta["context"].(string); ctx == "log_message" {
			logStrings = append(logStrings, n)
		}
	}
	if len(logStrings) != 1 {
		t.Fatalf("expected 1 deduped KindString log_message, got %d", len(logStrings))
	}
	emits := 0
	for _, e := range fix.edgesByKind[graph.EdgeEmits] {
		if e.To == logStrings[0].ID {
			emits++
		}
	}
	if emits != 2 {
		t.Errorf("expected 2 EdgeEmits (one per caller), got %d", emits)
	}
}
