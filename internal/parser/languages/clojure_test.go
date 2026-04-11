package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestClojureExtractor_Functions(t *testing.T) {
	src := []byte(`(ns myapp.core)

(defn greet [name]
  (str "Hello, " name))

(defn- helper [x]
  (+ x 1))
`)
	e := NewClojureExtractor()
	result, err := e.Extract("core.clj", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "greet")
	assert.Contains(t, names, "helper")
}

func TestClojureExtractor_TypesAndImports(t *testing.T) {
	src := []byte(`(ns myapp.models
  (:require [clojure.string :as str]
            [myapp.db :as db]))

(defprotocol Renderable
  (render [this]))

(defrecord User [name email])
`)
	e := NewClojureExtractor()
	result, err := e.Extract("models.clj", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	typeNames := make([]string, len(types))
	for i, n := range types {
		typeNames[i] = n.Name
	}
	assert.Contains(t, typeNames, "User")

	interfaces := nodesOfKind(result.Nodes, graph.KindInterface)
	assert.Len(t, interfaces, 1)
	assert.Equal(t, "Renderable", interfaces[0].Name)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.GreaterOrEqual(t, len(imports), 1)
}

func TestClojureExtractor_Variables(t *testing.T) {
	src := []byte(`(ns myapp.config)

(def version "1.0.0")
(def max-retries 3)
`)
	e := NewClojureExtractor()
	result, err := e.Extract("config.clj", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	varNames := make([]string, len(vars))
	for i, v := range vars {
		varNames[i] = v.Name
	}
	assert.Contains(t, varNames, "version")
	assert.Contains(t, varNames, "max-retries")
}
