package indexer

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/parser/tsalias"
)

func TestTSFileCandidates(t *testing.T) {
	got := tsFileCandidates("src/components/Widget.ts")
	for _, want := range []string{
		"src/components/Widget.ts",
		"src/components/Widget.tsx",
		"src/components/Widget/index.ts",
		"src/components/Widget/index.tsx",
	} {
		require.Contains(t, got, want)
	}
	require.Nil(t, tsFileCandidates(""))
}

func TestParseTSReExports_StarNamedAndType(t *testing.T) {
	src := `
export * from './widgets';
export { Button, Icon as Glyph } from './ui';
export type { Theme } from './theme';
export * as utils from './util';
export const local = 1;
`
	res := parseTSReExports(src, "src/index.ts", nil, "")
	// star + named + type re-exports — the `* as utils` namespace
	// re-export and the local const are not forwarding statements.
	require.Len(t, res, 3)

	byFile := map[string]tsReExport{}
	for _, re := range res {
		byFile[re.fromFile] = re
	}
	require.True(t, byFile["src/widgets.ts"].star)
	require.Equal(t, "Button", byFile["src/ui.ts"].names["Button"])
	require.Equal(t, "Icon", byFile["src/ui.ts"].names["Glyph"]) // `as` rebind
	require.Equal(t, "Theme", byFile["src/theme.ts"].names["Theme"])
}

func TestFollowReExportChain_BarrelToTerminal(t *testing.T) {
	mi := &MultiIndexer{}
	srcCache := map[string][]byte{
		"src/index.ts":             []byte(`export { Widget } from './components';`),
		"src/components/index.ts":  []byte(`export * from './Widget';`),
		"src/components/Widget.ts": []byte(`export interface Widget { id: string }`),
	}
	reachable := mi.followReExportChain("src/index.ts", "Widget", srcCache)
	require.True(t, reachable["src/components/Widget.ts"],
		"re-export chain must reach the terminal module")
}

func TestFollowReExportChain_RenamedExport(t *testing.T) {
	mi := &MultiIndexer{}
	srcCache := map[string][]byte{
		"pkg/index.ts": []byte(`export { Internal as Public } from './impl';`),
		"pkg/impl.ts":  []byte(`export class Internal {}`),
	}
	reachable := mi.followReExportChain("pkg/index.ts", "Public", srcCache)
	require.True(t, reachable["pkg/impl.ts"], "must follow the `as` rename to the source module")
}

func TestFollowReExportChain_CircularTerminates(t *testing.T) {
	mi := &MultiIndexer{}
	srcCache := map[string][]byte{
		"a.ts": []byte(`export * from './b';`),
		"b.ts": []byte(`export * from './a';`),
	}
	reachable := mi.followReExportChain("a.ts", "X", srcCache)
	require.True(t, reachable["a.ts"])
	require.True(t, reachable["b.ts"])
}

// TestResolveTSModulePath_AliasResolvesWithPrefix wires an alias map
// (matching what tsalias.Load would produce for a Next.js-style
// `@/*` config) into resolveTSModulePath and verifies the result is
// re-prefixed with the repo prefix so the caller can match it
// against the graph node's FilePath.
func TestResolveTSModulePath_AliasResolvesWithPrefix(t *testing.T) {
	m := &tsalias.Map{
		Entries: []tsalias.Alias{
			{AliasPrefix: "@/", TargetPrefix: "src/", HasWildcard: true},
		},
	}
	got := resolveTSModulePath("@/lib/api", "myrepo/src/components", m, "myrepo")
	want := "myrepo/src/lib/api.ts"
	if got != want {
		t.Errorf("resolveTSModulePath alias = %q, want %q", got, want)
	}
}

// TestResolveTSModulePath_AliasNoMatchFallsThrough ensures that a bare
// specifier with no matching alias entry still returns "" so the
// caller leaves the bare-name reference in place.
func TestResolveTSModulePath_AliasNoMatchFallsThrough(t *testing.T) {
	m := &tsalias.Map{
		Entries: []tsalias.Alias{
			{AliasPrefix: "@/", TargetPrefix: "src/", HasWildcard: true},
		},
	}
	if got := resolveTSModulePath("react", "myrepo/src", m, "myrepo"); got != "" {
		t.Errorf("unmatched bare specifier should resolve to \"\", got %q", got)
	}
}

// TestParseTSImports_AliasResolvesNamedImport drives the full path
// from import-statement scanning through alias resolution.
func TestParseTSImports_AliasResolvesNamedImport(t *testing.T) {
	src := `
import { Foo } from '@/lib/schema'
`
	m := &tsalias.Map{
		Entries: []tsalias.Alias{
			{AliasPrefix: "@/", TargetPrefix: "src/", HasWildcard: true},
		},
	}
	got := parseTSImports(src, "web/src/app/page.tsx", m, "web")
	want := map[string]string{"Foo": "web/src/lib/schema.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// TestParseTSImports_ResolvesNamedImports covers the common shape:
// `import type { Foo, Bar as Baz } from './schema'`. Each named
// binding maps to the resolved file path; the alias rebind uses
// the local-binding name (`Baz`), not the source name (`Bar`).
func TestParseTSImports_ResolvesNamedImports(t *testing.T) {
	src := `
import type { Foo, Bar as Baz } from './schema'
import { Quux } from '../shared/types'
`
	got := parseTSImports(src, "web/src/lib/api.ts", nil, "")
	want := map[string]string{
		"Foo":  "web/src/lib/schema.ts",
		"Baz":  "web/src/lib/schema.ts",
		"Quux": "web/src/shared/types.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// TestParseTSImports_SkipsBareSpecifiers ensures third-party and
// path-aliased imports (`react`, `@/lib/foo`) don't leak into the
// resolution map — they don't correspond to a graph file in the
// local repo and would mislead the disambiguator if they did.
func TestParseTSImports_SkipsBareSpecifiers(t *testing.T) {
	src := `
import React from 'react'
import { useState } from 'react'
import { foo } from '@/lib/foo'
`
	got := parseTSImports(src, "web/src/lib/api.ts", nil, "")
	if len(got) != 0 {
		t.Errorf("expected empty map for bare specifiers, got %+v", got)
	}
}

// TestParseTSImports_DefaultAndNamespace checks `import Foo from`
// (default) and `import * as NS from` bindings.
func TestParseTSImports_DefaultAndNamespace(t *testing.T) {
	src := `
import Default from './a'
import * as NS from './b'
`
	got := parseTSImports(src, "x/y.ts", nil, "")
	if got["Default"] != "x/a.ts" {
		t.Errorf("default import: got %q, want %q", got["Default"], "x/a.ts")
	}
	if got["NS"] != "x/b.ts" {
		t.Errorf("namespace import: got %q, want %q", got["NS"], "x/b.ts")
	}
}

// TestSplitTSImportClause_AliasAndType normalises the `type` keyword
// and `as <local>` rebind in named-import bodies.
func TestSplitTSImportClause_AliasAndType(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"Foo, Bar", []string{"Foo", "Bar"}},
		{"type Foo, Bar as Baz", []string{"Foo", "Baz"}},
		{"  Foo  ,  type Bar  ", []string{"Foo", "Bar"}},
		{"", nil},
	}
	for _, c := range cases {
		got := splitTSImportClause(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitTSImportClause(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestResolveTSModulePath_AddsTSExtension confirms that a relative
// specifier without an extension picks up `.ts` so a candidate
// living in `<dir>/file.ts` is matchable. Specifiers with an
// explicit extension are left alone.
func TestResolveTSModulePath_AddsTSExtension(t *testing.T) {
	cases := []struct {
		mod, dir, want string
	}{
		{"./schema", "web/src/lib", "web/src/lib/schema.ts"},
		{"./schema.ts", "web/src/lib", "web/src/lib/schema.ts"},
		{"./schema.tsx", "web/src/lib", "web/src/lib/schema.tsx"},
		{"../api", "web/src/lib/inner", "web/src/lib/api.ts"},
		{"react", "web/src/lib", ""},
		{"@/lib/foo", "web/src/lib", ""},
	}
	for _, c := range cases {
		got := resolveTSModulePath(c.mod, c.dir, nil, "")
		if got != c.want {
			t.Errorf("resolveTSModulePath(%q, %q) = %q, want %q", c.mod, c.dir, got, c.want)
		}
	}
}

// TestIsImportResolvableLang only flags TS / JS family extensions —
// Go / Python / etc. are skipped because their import semantics
// differ enough that this resolver would mis-attribute candidates.
func TestIsImportResolvableLang(t *testing.T) {
	pos := []string{"a.ts", "b.tsx", "c.js", "d.jsx", "e.mts", "f.cjs"}
	neg := []string{"a.go", "b.py", "c.rb", "d.java", "e", "f.kt"}
	for _, p := range pos {
		if !isImportResolvableLang(p) {
			t.Errorf("%q: want true, got false", p)
		}
	}
	for _, p := range neg {
		if isImportResolvableLang(p) {
			t.Errorf("%q: want false, got true", p)
		}
	}
}
