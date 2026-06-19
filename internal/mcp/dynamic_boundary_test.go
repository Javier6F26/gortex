package mcp

import (
	"strings"
	"testing"
)

// TestDynamicBoundaryEmitsSiteFormKeyCandidates proves the dispatch-boundary
// detector: each runtime-dispatch form in a symbol body produces a boundary
// carrying the {site file:line, form, key, candidate shortlist}, agent-named
// candidates are flagged, and a dispatch token inside a comment is ignored.
func TestDynamicBoundaryEmitsSiteFormKeyCandidates(t *testing.T) {
	body := strings.Join([]string{
		"def route(self, name, payload):",            // line 10 (startLine)
		"    handler = getattr(self, name)",           // 11 reflection, key=name
		"    return self.handlers[action](payload)",   // 12 computed_member, key=action
		"    # self.bus.emit('ignored.in.comment')",   // 13 comment — must be skipped
		"    self.bus.emit('user.created', payload)",  // 14 event_bus, key=user.created
	}, "\n")

	// Stub candidate resolver: 'name' resolves to two handler symbols, one of
	// which is agent-named; everything else resolves to nothing.
	resolve := func(form, key string) []string {
		if key == "name" {
			return []string{"svc.py::DefaultHandler", "svc.py::orderProcessor"}
		}
		return nil
	}

	got := detectDynamicBoundaries(body, "svc.py", 10, resolve)

	byForm := map[string]DynamicBoundary{}
	for _, b := range got {
		byForm[b.Form] = b
	}

	// Reflection boundary: site/key + candidate shortlist + agent flag.
	refl, ok := byForm[dispatchFormReflection]
	if !ok {
		t.Fatalf("no reflection boundary detected; got %d boundaries: %+v", len(got), got)
	}
	if refl.Key != "name" {
		t.Errorf("reflection key=%q, want name", refl.Key)
	}
	if refl.Site != "svc.py:11" {
		t.Errorf("reflection site=%q, want svc.py:11", refl.Site)
	}
	if len(refl.Candidates) != 2 {
		t.Errorf("reflection candidates=%v, want 2", refl.Candidates)
	}
	if !refl.AgentNamed {
		t.Error("reflection boundary should be flagged agent_named (orderProcessor)")
	}

	// Computed-member boundary.
	cm, ok := byForm[dispatchFormComputedMember]
	if !ok {
		t.Fatal("no computed_member boundary detected")
	}
	if cm.Key != "action" {
		t.Errorf("computed_member key=%q, want action", cm.Key)
	}
	if cm.Site != "svc.py:12" {
		t.Errorf("computed_member site=%q, want svc.py:12", cm.Site)
	}

	// Event-bus boundary at line 14 (NOT the commented one at 13).
	eb, ok := byForm[dispatchFormEventBus]
	if !ok {
		t.Fatal("no event_bus boundary detected")
	}
	if eb.Key != "user.created" {
		t.Errorf("event_bus key=%q, want user.created", eb.Key)
	}
	if eb.Site != "svc.py:14" {
		t.Errorf("event_bus site=%q, want svc.py:14 (the commented dispatch at :13 must be skipped)", eb.Site)
	}

	// The commented dispatch must not have produced a boundary.
	for _, b := range got {
		if b.Site == "svc.py:13" {
			t.Errorf("a commented-out dispatch was wrongly emitted: %+v", b)
		}
	}
}
