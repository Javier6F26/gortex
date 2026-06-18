package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
	"github.com/zzet/gortex/internal/parser"
)

// anonType returns the single anonymous synthetic type node and its outgoing
// EdgeExtends target, or fails the test if either is missing.
func anonTypeAndExtends(t *testing.T, res *parser.ExtractionResult) (*graph.Node, string) {
	t.Helper()
	var anon *graph.Node
	for _, n := range res.Nodes {
		if n.Kind == graph.KindType && n.Meta != nil {
			if v, _ := n.Meta["anonymous"].(bool); v {
				if anon != nil {
					t.Fatalf("expected exactly one anonymous type, found a second: %s", n.ID)
				}
				anon = n
			}
		}
	}
	if anon == nil {
		t.Fatal("no anonymous synthetic type node was extracted")
	}
	var extendsTo string
	for _, e := range res.Edges {
		if e.From == anon.ID && e.Kind == graph.EdgeExtends {
			extendsTo = e.To
		}
	}
	if extendsTo == "" {
		t.Fatalf("anonymous type %s has no EdgeExtends", anon.ID)
	}
	return anon, extendsTo
}

func TestJavaExtractor_AnonymousClass(t *testing.T) {
	const src = `package com.app;

class Host {
    void wire() {
        Runnable r = new Runnable() {
            public void run() {
                System.out.println("tick");
            }
        };
        r.run();
    }
}
`
	res, err := NewJavaExtractor().Extract("Host.java", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	anon, extendsTo := anonTypeAndExtends(t, res)
	if anon.Language != "java" {
		t.Errorf("anonymous type language = %q, want java", anon.Language)
	}
	if want := "unresolved::Runnable"; extendsTo != want {
		t.Errorf("anonymous class extends %q, want %q", extendsTo, want)
	}
	if sp, _ := anon.Meta["scope_parent"].(string); sp != "Runnable" {
		t.Errorf("scope_parent = %q, want Runnable", sp)
	}
}

func TestCSharpExtractor_AnonymousClass(t *testing.T) {
	const src = `namespace App;

class Host {
    void Wire() {
        var p = new { Name = "x", Age = 5 };
        System.Console.WriteLine(p.Name);
    }
}
`
	res, err := NewCSharpExtractor().Extract("Host.cs", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	anon, extendsTo := anonTypeAndExtends(t, res)
	if anon.Language != "csharp" {
		t.Errorf("anonymous type language = %q, want csharp", anon.Language)
	}
	if want := "unresolved::object"; extendsTo != want {
		t.Errorf("anonymous type extends %q, want %q", extendsTo, want)
	}
}
