package main

import (
	"errors"
	"testing"
)

func TestVectorBuildNote(t *testing.T) {
	if got := vectorBuildNote(nil); got != "no vector data after indexing" {
		t.Fatalf("nil-error note = %q, want the generic empty-corpus note", got)
	}
	got := vectorBuildNote(errors.New("chunk embedding failed: boom"))
	if want := "vector build failed: chunk embedding failed: boom"; got != want {
		t.Fatalf("error note = %q, want %q", got, want)
	}
}
