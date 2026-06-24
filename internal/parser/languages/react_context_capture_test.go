package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func reactContextRef(edges []*graph.Edge, from, ctx string) *graph.Edge {
	for _, e := range edges {
		if e.Kind != graph.EdgeReferences || e.From != from || e.To != "unresolved::"+ctx {
			continue
		}
		if v, _ := e.Meta["via"].(string); v == "react_context" {
			return e
		}
	}
	return nil
}

func TestReactContextCapture_BindsArgToEnclosingComponent(t *testing.T) {
	src := `import { useContext } from 'react';

export function Profile() {
  const theme = useContext(ThemeContext);
  return null;
}
`
	res, err := NewTypeScriptExtractor().Extract("src/components/Profile.tsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ref := reactContextRef(res.Edges, "src/components/Profile.tsx::Profile", "ThemeContext")
	if ref == nil {
		t.Fatalf("expected a react_context reference to ThemeContext from Profile")
	}
	if cn, _ := ref.Meta["context_name"].(string); cn != "ThemeContext" {
		t.Errorf("context_name = %q (want ThemeContext)", cn)
	}
}

func TestReactContextCapture_NonIdentArgIgnored(t *testing.T) {
	// `useContext(theme.ctx)` — a member-expression argument is not a bare
	// context object and must not produce a placeholder reference.
	src := `export function Panel() {
  const v = useContext(theme.ctx);
  return null;
}
`
	res, err := NewTypeScriptExtractor().Extract("src/components/Panel.tsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeReferences && e.Meta != nil {
			if v, _ := e.Meta["via"].(string); v == "react_context" {
				t.Errorf("member-expr useContext arg should not emit a react_context ref, got %s", e.To)
			}
		}
	}
}

func TestReactContextCapture_JavaScriptExtractor(t *testing.T) {
	src := `export function Widget() {
  const c = useContext(LocaleContext);
  return null;
}
`
	res, err := NewJavaScriptExtractor().Extract("src/widget.jsx", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if reactContextRef(res.Edges, "src/widget.jsx::Widget", "LocaleContext") == nil {
		t.Fatalf("expected a react_context reference to LocaleContext from Widget (JS extractor)")
	}
}
