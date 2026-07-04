package mcp

import "testing"

func symEntry(id string, line int, source string) map[string]any {
	return map[string]any{"id": id, "start_line": line, "source": source, "name": id}
}

func packResult(syms ...map[string]any) map[string]any {
	list := make([]map[string]any, len(syms))
	copy(list, syms)
	return map[string]any{
		"task":             "demo",
		"relevant_symbols": list,
		"files_to_edit":    []string{"a.go"},
	}
}

func TestExtractPackViewAndDiff(t *testing.T) {
	prior := extractPackView(packResult(
		symEntry("a.go::A", 1, "func A() {}"),
		symEntry("b.go::B", 5, "func B() {}"),
	), false)

	current := extractPackView(packResult(
		symEntry("a.go::A", 1, "func A() {}"),          // unchanged
		symEntry("b.go::B", 5, "func B() { changed }"), // changed body
		symEntry("c.go::C", 9, "func C() {}"),          // added
	), true)

	delta := diffPackViews(prior, current, "root1", "root2")

	if got := delta["base_root"].(string); got != "root1" {
		t.Fatalf("base_root = %q", got)
	}
	if added, _ := delta["added"].([]map[string]any); len(added) != 1 || added[0]["id"] != "c.go::C" {
		t.Fatalf("added = %v, want [c.go::C]", delta["added"])
	}
	if changed, _ := delta["changed"].([]map[string]any); len(changed) != 1 || changed[0]["id"] != "b.go::B" {
		t.Fatalf("changed = %v, want [b.go::B]", delta["changed"])
	}
	if removed, _ := delta["removed"].([]string); len(removed) != 0 {
		t.Fatalf("removed = %v, want []", removed)
	}
	if uc := delta["unchanged_count"].(int); uc != 1 {
		t.Fatalf("unchanged_count = %d, want 1", uc)
	}
}

func TestDiffRemovedSymbol(t *testing.T) {
	prior := extractPackView(packResult(
		symEntry("a.go::A", 1, "x"),
		symEntry("b.go::B", 2, "y"),
	), false)
	current := extractPackView(packResult(
		symEntry("a.go::A", 1, "x"),
	), true)
	delta := diffPackViews(prior, current, "r1", "r2")
	removed, _ := delta["removed"].([]string)
	if len(removed) != 1 || removed[0] != "b.go::B" {
		t.Fatalf("removed = %v, want [b.go::B]", removed)
	}
}

func TestDiffWorthItTokens(t *testing.T) {
	// A large prior pack, a current pack identical except one added
	// small symbol -> delta should be far smaller than the full pack.
	big := make([]map[string]any, 0, 20)
	body := "func F() { /* a reasonably long body to make tokens count */ return }"
	for i := 0; i < 20; i++ {
		big = append(big, symEntry("pkg/f.go::F"+string(rune('A'+i)), i, body))
	}
	prior := extractPackView(packResult(big...), false)
	current := extractPackView(packResult(append(append([]map[string]any{}, big...), symEntry("pkg/g.go::G", 99, "x"))...), true)
	delta := diffPackViews(prior, current, "r1", "r2")
	if worth, _ := delta["worth_it"].(bool); !worth {
		t.Fatalf("expected worth_it=true for a one-symbol delta over a 20-symbol pack; delta_tokens=%v full_tokens=%v",
			delta["delta_tokens"], delta["full_tokens"])
	}
}

func TestPackDeltaCacheLRU(t *testing.T) {
	c := newPackDeltaCache()
	c.cap = 2
	c.put("r1", extractPackView(packResult(symEntry("a", 1, "x")), false))
	c.put("r2", extractPackView(packResult(symEntry("b", 1, "y")), false))
	if _, ok := c.get("r1"); !ok {
		t.Fatal("r1 should be present")
	}
	c.put("r3", extractPackView(packResult(symEntry("c", 1, "z")), false))
	if _, ok := c.get("r2"); ok {
		t.Fatal("r2 should have been evicted")
	}
	if _, ok := c.get("r1"); !ok {
		t.Fatal("r1 should survive")
	}
}

func TestExtractPackViewGradedManifest(t *testing.T) {
	result := map[string]any{
		"task": "demo",
		"context_manifest": map[string]any{
			"entries": []map[string]any{
				symEntry("a.go::A", 1, "x"),
				symEntry("b.go::B", 2, "y"),
			},
		},
		// relevant_symbols should be ignored when a manifest is present.
		"relevant_symbols": []map[string]any{symEntry("z.go::Z", 9, "ignored")},
	}
	v := extractPackView(result, false)
	if len(v.Symbols) != 2 {
		t.Fatalf("expected 2 manifest symbols, got %d", len(v.Symbols))
	}
	for _, s := range v.Symbols {
		if s.ID == "z.go::Z" {
			t.Fatal("relevant_symbols must be ignored when manifest present")
		}
	}
}
