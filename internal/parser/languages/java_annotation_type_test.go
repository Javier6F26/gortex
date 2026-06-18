package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestJavaExtractor_AnnotationType(t *testing.T) {
	const java = `package com.app;

public @interface Audited {
    String value() default "";
    int level() default 0;
}
`
	res, err := NewJavaExtractor().Extract("Audited.java", []byte(java))
	if err != nil {
		t.Fatal(err)
	}
	var node *graph.Node
	for _, n := range res.Nodes {
		if n.Name == "Audited" {
			node = n
		}
	}
	if node == nil {
		t.Fatal("annotation type 'Audited' was not extracted")
	}
	if node.Kind != graph.KindInterface {
		t.Errorf("@interface Audited should be a KindInterface, got %s", node.Kind)
	}
}
