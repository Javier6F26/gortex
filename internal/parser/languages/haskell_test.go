package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestHaskellExtractor_Functions(t *testing.T) {
	src := []byte(`module Main where

add :: Int -> Int -> Int
add x y = x + y

greet :: String -> String
greet name = "Hello, " ++ name
`)
	e := NewHaskellExtractor()
	result, err := e.Extract("Main.hs", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "greet")
}

func TestHaskellExtractor_TypesAndImports(t *testing.T) {
	src := []byte(`module Shapes where

import Data.List
import qualified Data.Map as Map

data Shape = Circle Double | Rectangle Double Double

newtype Name = Name String

type Point = (Double, Double)

class Drawable a where
  draw :: a -> String
`)
	e := NewHaskellExtractor()
	result, err := e.Extract("Shapes.hs", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	typeNames := make([]string, len(types))
	for i, n := range types {
		typeNames[i] = n.Name
	}
	assert.Contains(t, typeNames, "Shape")
	assert.Contains(t, typeNames, "Name")
	assert.Contains(t, typeNames, "Point")

	classes := nodesOfKind(result.Nodes, graph.KindInterface)
	assert.Len(t, classes, 1)
	assert.Equal(t, "Drawable", classes[0].Name)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.Len(t, imports, 2)
}

func TestHaskellExtractor_Module(t *testing.T) {
	src := []byte(`module MyLib.Utils where

helper x = x + 1
`)
	e := NewHaskellExtractor()
	result, err := e.Extract("Utils.hs", src)
	require.NoError(t, err)

	pkgs := nodesOfKind(result.Nodes, graph.KindPackage)
	require.Len(t, pkgs, 1)
	assert.Equal(t, "MyLib.Utils", pkgs[0].Name)
}
