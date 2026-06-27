package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestLuaExtractor_Functions(t *testing.T) {
	src := []byte(`
function greet(name)
  print("Hello, " .. name)
end

function add(a, b)
  return a + b
end

local function helper()
  return 42
end
`)
	e := NewLuaExtractor()
	result, err := e.Extract("utils.lua", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	names := make([]string, len(funcs))
	for i, f := range funcs {
		names[i] = f.Name
	}
	assert.Contains(t, names, "greet")
	assert.Contains(t, names, "add")
	assert.Contains(t, names, "helper")
}

func TestLuaExtractor_Methods(t *testing.T) {
	src := []byte(`
local M = {}

function M.init()
  M.value = 0
end

function M.getValue()
  return M.value
end
`)
	e := NewLuaExtractor()
	result, err := e.Extract("module.lua", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.GreaterOrEqual(t, len(methods), 2)
	for _, m := range methods {
		assert.Equal(t, "M", m.Meta["receiver"])
	}

	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	require.GreaterOrEqual(t, len(memberEdges), 2)
}

func TestLuaExtractor_Variables(t *testing.T) {
	src := []byte(`
local version = "1.0"
local count = 42
`)
	e := NewLuaExtractor()
	result, err := e.Extract("config.lua", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	require.Len(t, vars, 2)
}

func TestLuaExtractor_Require(t *testing.T) {
	src := []byte(`
require("utils")
require("lib.json")

local x = 1
`)
	e := NewLuaExtractor()
	result, err := e.Extract("main.lua", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	require.Len(t, imports, 2)
}

func TestLuaExtractor_CallSites(t *testing.T) {
	src := []byte(`
function main()
  print("hello")
  greet("world")
end
`)
	e := NewLuaExtractor()
	result, err := e.Extract("main.lua", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	assert.GreaterOrEqual(t, len(calls), 2)
}

// TestLuaRobloxInstanceRequires is the C7 test: Roblox / Luau instance-path
// requires (script.Parent.X, game.Service.X, script:WaitForChild("X")) resolve
// to import edges keyed by the module leaf, with the full instance path kept on
// the edge — and the same shared resolver drives both the Lua and Luau
// extractors. Classic string requires keep working.
func TestLuaRobloxInstanceRequires(t *testing.T) {
	src := []byte("local A = require(script.Parent.Module)\n" +
		"local B = require(game.ReplicatedStorage.Shared.Foo)\n" +
		"local C = require(script:WaitForChild(\"Bar\"))\n" +
		"local D = require(\"std.path\")\n")

	check := func(t *testing.T, edges []*graph.Edge) {
		imports := map[string]string{} // target -> roblox_path
		for _, e := range edges {
			if e.Kind == graph.EdgeImports {
				p := ""
				if e.Meta != nil {
					p, _ = e.Meta["roblox_path"].(string)
				}
				imports[e.To] = p
			}
		}
		assert.Equal(t, "script.Parent.Module", imports["unresolved::import::Module"], "instance-path leaf + path")
		assert.Equal(t, "game.ReplicatedStorage.Shared.Foo", imports["unresolved::import::Foo"])
		assert.Equal(t, "script:WaitForChild(\"Bar\")", imports["unresolved::import::Bar"], "WaitForChild leaf")
		if _, ok := imports["unresolved::import::std.path"]; !ok {
			t.Fatalf("classic string require should still resolve; got %v", imports)
		}
	}

	lua, err := NewLuaExtractor().Extract("m.lua", src)
	require.NoError(t, err)
	t.Run("lua", func(t *testing.T) { check(t, lua.Edges) })

	luau, err := NewLuauExtractor().Extract("m.luau", src)
	require.NoError(t, err)
	t.Run("luau", func(t *testing.T) { check(t, luau.Edges) })
}

func TestLuaExtractor_FnValueCapture(t *testing.T) {
	src := []byte("function onClick() end\n" +
		"local handlers = {}\n" +
		"function handlers.onTick() end\n" +
		"function run()\n" +
		"  setCallback(onClick)\n" +
		"  register(handlers.onTick)\n" +
		"end\n")
	res, err := NewLuaExtractor().Extract("s.lua", []byte(src))
	require.NoError(t, err)

	cands := map[string]bool{}
	for _, e := range res.Edges {
		if e.Meta == nil {
			continue
		}
		if v, _ := e.Meta["via"].(string); v != "callback_candidate" {
			continue
		}
		if name, _ := e.Meta["fn_value_name"].(string); name != "" {
			cands[name] = true
			assert.Equal(t, "s.lua::run", e.From, "captured in the enclosing function")
		}
	}
	assert.True(t, cands["onClick"], "setCallback(onClick) should capture onClick (got %v)", keys2(cands))
	assert.True(t, cands["onTick"], "register(handlers.onTick) should capture onTick (got %v)", keys2(cands))
}

func keys2(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
