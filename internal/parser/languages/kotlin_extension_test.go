package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestKotlinExtractor_ExtensionReceiver(t *testing.T) {
	const kt = `package com.app

class Point(val x: Int, val y: Int)

fun Point.magnitude(): Int {
    return x + y
}

fun <T> List<T>.secondOrNull(): T? {
    return null
}
`
	res, err := NewKotlinExtractor().Extract("Ext.kt", []byte(kt))
	if err != nil {
		t.Fatal(err)
	}

	var mag, second *graph.Node
	for _, n := range res.Nodes {
		switch n.Name {
		case "magnitude":
			mag = n
		case "secondOrNull":
			second = n
		}
	}

	// A simple extension attaches to its receiver type as a method.
	if mag == nil {
		t.Fatal("extension fun 'magnitude' was not extracted")
	}
	if mag.Kind != graph.KindMethod {
		t.Errorf("magnitude should be a method, got %s", mag.Kind)
	}
	if mag.Meta["receiver"] != "Point" {
		t.Errorf("magnitude receiver = %v, want Point", mag.Meta["receiver"])
	}
	var memberEdge bool
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeMemberOf && e.From == mag.ID && e.To == "Ext.kt::Point" {
			memberEdge = true
		}
	}
	if !memberEdge {
		t.Errorf("magnitude should be member_of Point; id=%s", mag.ID)
	}

	// A generic extension's receiver is reduced to the base type.
	if second == nil {
		t.Fatal("extension fun 'secondOrNull' was not extracted")
	}
	if second.Meta["receiver"] != "List" {
		t.Errorf("secondOrNull receiver = %v, want List", second.Meta["receiver"])
	}
}
