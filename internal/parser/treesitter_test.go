package parser

import (
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseFile_Go(t *testing.T) {
	src := []byte(`package main

func Hello() {
	fmt.Println("hello")
}
`)
	lang := grammars.GoLanguage()
	tree, err := ParseFile(src, lang)
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	assert.Equal(t, "source_file", NodeType(root, lang))
	assert.True(t, root.ChildCount() > 0)
}

func TestRunQuery_GoFunction(t *testing.T) {
	src := []byte(`package main

func Hello() {}
func World(x int) string { return "" }
`)
	lang := grammars.GoLanguage()
	tree, err := ParseFile(src, lang)
	require.NoError(t, err)
	defer tree.Close()

	pattern := `(function_declaration name: (identifier) @func.name) @func.def`
	results, err := RunQuery(pattern, lang, tree.RootNode(), src)
	require.NoError(t, err)
	require.Len(t, results, 2)

	assert.Equal(t, "Hello", results[0].Captures["func.name"].Text)
	assert.Equal(t, "World", results[1].Captures["func.name"].Text)
}

func TestRunQuery_NoMatches(t *testing.T) {
	src := []byte(`package main

var x = 42
`)
	lang := grammars.GoLanguage()
	tree, err := ParseFile(src, lang)
	require.NoError(t, err)
	defer tree.Close()

	pattern := `(function_declaration name: (identifier) @func.name) @func.def`
	results, err := RunQuery(pattern, lang, tree.RootNode(), src)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestParseFile_InvalidSource(t *testing.T) {
	// tree-sitter is error-tolerant; it returns a tree even for garbage input.
	src := []byte(`{{{{{not valid go at all!!!!`)
	tree, err := ParseFile(src, grammars.GoLanguage())
	require.NoError(t, err)
	defer tree.Close()

	root := tree.RootNode()
	assert.NotNil(t, root) // just verify it doesn't crash
}
