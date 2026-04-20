//go:build golden

// Command generate emits GCX golden fixtures consumed by the
// @gortex/wire TypeScript tests. Build tag `golden` keeps it out of
// regular Go builds.
//
//	go run -tags golden packages/wire-ts/test/golden/generate.go
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zzet/gortex/pkg/wire"
)

func main() {
	outDir := "packages/wire-ts/test/golden"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	must(os.MkdirAll(outDir, 0o755))

	must(writeSearchSymbols(filepath.Join(outDir, "search_symbols.gcx")))
	must(writeGetCallers(filepath.Join(outDir, "get_callers.gcx")))
	must(writeGetSymbolSource(filepath.Join(outDir, "get_symbol_source.gcx")))
	fmt.Fprintln(os.Stderr, "wrote golden fixtures to", outDir)
}

func writeSearchSymbols(path string) error {
	var buf bytes.Buffer
	enc := wire.NewEncoder(&buf, wire.Header{
		Tool:   "search_symbols",
		Fields: []string{"id", "kind", "name", "path", "line", "sig"},
		Meta:   map[string]string{"total": "5", "truncated": "false"},
	})
	rows := []struct {
		id, kind, name, path string
		line                 int
		sig                  string
	}{
		{"a.go::Foo", "function", "Foo", "a.go", 10, "func Foo()"},
		{"a.go::Bar", "function", "Bar", "a.go", 20, "func Bar(x int) error"},
		{"b.go::Baz", "method", "Baz", "b.go", 30, "func (s *Svc) Baz()"},
		{"c.go::Qux", "type", "Qux", "c.go", 40, "type Qux struct { A int }"},
		{"d.go::Quux", "function", "Quux", "d.go", 50, "func Quux(ctx context.Context) error"},
	}
	for _, r := range rows {
		if err := enc.WriteRow(r.id, r.kind, r.name, r.path, r.line, r.sig); err != nil {
			return err
		}
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeGetCallers(path string) error {
	var buf bytes.Buffer
	nodes := wire.NewEncoder(&buf, wire.Header{
		Tool:   "get_callers.nodes",
		Fields: []string{"id", "kind", "name", "path", "line"},
	})
	_ = nodes.WriteRow("a.go::Caller1", "function", "Caller1", "a.go", 5)
	_ = nodes.WriteRow("b.go::Caller2", "method", "Caller2", "b.go", 15)
	if err := nodes.Close(); err != nil {
		return err
	}
	edges := wire.NewEncoder(&buf, wire.Header{
		Tool:   "get_callers.edges",
		Fields: []string{"from", "to", "kind", "origin", "confidence"},
	})
	if err := edges.WriteRow("a.go::Caller1", "target", "calls", "ast_resolved", 1.0); err != nil {
		return err
	}
	if err := edges.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeGetSymbolSource(path string) error {
	src := "func Foo() {\n\tfmt.Println(\"x\\ty\")\n}"
	var buf bytes.Buffer
	enc := wire.NewEncoder(&buf, wire.Header{
		Tool:   "get_symbol_source",
		Fields: []string{"id", "source"},
		Meta:   map[string]string{"etag": "etag-test"},
	})
	if err := enc.WriteRow("a.go::Foo", src); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}
