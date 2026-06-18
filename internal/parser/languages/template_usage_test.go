package languages

import (
	"strings"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestTemplateUsage(t *testing.T) {
	const sfc = `<script setup lang="ts">
import Counter from './Counter.vue'
function noop() {}
</script>

<template>
  <div class="wrap">
    <Counter :start="0" />
    <user-card name="x" />
    <button @click="noop">plain html</button>
    <Teleport to="body"><Modal /></Teleport>
  </div>
</template>
`
	res, err := NewVueExtractor().Extract("App.vue", []byte(sfc))
	if err != nil {
		t.Fatal(err)
	}

	refs := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && strings.HasPrefix(e.To, "unresolved::") {
			refs[strings.TrimPrefix(e.To, "unresolved::")] = true
		}
	}

	// Component tags become cross-file references; kebab-case is PascalCased.
	for _, want := range []string{"Counter", "UserCard", "Modal"} {
		if !refs[want] {
			t.Errorf("missing component reference %q (got refs: %v)", want, refs)
		}
	}
	// Framework builtins and plain HTML elements are not references.
	if refs["Teleport"] {
		t.Error("framework builtin <Teleport> should be skipped")
	}
	if refs["Button"] || refs["Div"] || refs["button"] {
		t.Error("plain HTML elements should be skipped")
	}
}

// TestTemplateUsageTwoRenderSitesPositioned proves the upgrade over a
// name-deduplicated single reference: rendering the same child component at two
// template locations emits TWO positioned edges — distinct line numbers, each
// AST-resolved provenance, each carrying the template role — so find_usages can
// report every render site rather than collapsing them into one position-less
// reference.
func TestTemplateUsageTwoRenderSitesPositioned(t *testing.T) {
	const sfc = `<script setup lang="ts">
import Counter from './Counter.vue'
</script>

<template>
  <div>
    <Counter :start="0" />
    <Counter :start="1" />
  </div>
</template>
`
	res, err := NewVueExtractor().Extract("App.vue", []byte(sfc))
	if err != nil {
		t.Fatal(err)
	}

	var sites []*graph.Edge
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && e.To == "unresolved::Counter" {
			sites = append(sites, e)
		}
	}
	if len(sites) != 2 {
		t.Fatalf("expected 2 positioned render edges for Counter, got %d", len(sites))
	}

	lines := map[int]bool{}
	for _, e := range sites {
		if e.Line == 0 {
			t.Errorf("render edge for Counter has no line number")
		}
		if e.Origin != graph.OriginASTResolved {
			t.Errorf("render edge at line %d origin=%v, want OriginASTResolved", e.Line, e.Origin)
		}
		if isTmpl, _ := e.Meta["template"].(bool); !isTmpl {
			t.Errorf("render edge at line %d missing Meta[template]=true", e.Line)
		}
		if got := graph.RefContextOf(e, graph.KindType); got != graph.RefContextTemplate {
			t.Errorf("render edge at line %d ref_context=%q, want %q", e.Line, got, graph.RefContextTemplate)
		}
		lines[e.Line] = true
	}
	if len(lines) != 2 {
		t.Errorf("expected 2 distinct render-site lines, got %v", lines)
	}
}
