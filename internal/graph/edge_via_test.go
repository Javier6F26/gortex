package graph

import "testing"

func TestViaLabelFor(t *testing.T) {
	cases := map[string]string{
		"swift.objc.bridge":  "Swift↔ObjC bridge",
		"observer.channel":   "observer channel",
		"closure.collection": "closure collection",
		"react.setstate":     "React setState",
		"flutter.setstate":   "Flutter setState",
		"kmp.expect-actual":  "KMP expect/actual",
		"":                   "",
		"unknown.synth":      "unknown.synth", // unmapped passes through
	}
	for in, want := range cases {
		if got := ViaLabelFor(in); got != want {
			t.Errorf("ViaLabelFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEdgeIdentityHashIgnoresVia(t *testing.T) {
	base := &Edge{From: "a", To: "b", Kind: EdgeCalls, Origin: OriginASTInferred}
	withVia := &Edge{From: "a", To: "b", Kind: EdgeCalls, Origin: OriginASTInferred, Via: "observer channel"}
	if base.IdentityHash() != withVia.IdentityHash() {
		t.Errorf("Via must not change the edge identity hash")
	}
}
