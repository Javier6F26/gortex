package languages

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestGoCgo_StampsUsesCgoOnFileNode(t *testing.T) {
	src := `package foo

/*
#include <stdio.h>
*/
import "C"

func Hello() {
	C.printf(C.CString("hello\n"))
}
`
	fix := runGoExtract(t, src)

	files := fix.nodesByKind[graph.KindFile]
	if len(files) != 1 {
		t.Fatalf("expected 1 file node, got %d", len(files))
	}
	if v, _ := files[0].Meta["uses_cgo"].(bool); !v {
		t.Errorf("expected meta.uses_cgo=true on file node with import \"C\"")
	}
}

func TestGoCgo_NoFlagOnRegularImport(t *testing.T) {
	src := `package foo

import "fmt"

func Hello() { fmt.Println("hi") }
`
	fix := runGoExtract(t, src)
	files := fix.nodesByKind[graph.KindFile]
	if len(files) != 1 {
		t.Fatalf("expected 1 file node")
	}
	if v, _ := files[0].Meta["uses_cgo"].(bool); v {
		t.Errorf("regular import must not set uses_cgo")
	}
}
