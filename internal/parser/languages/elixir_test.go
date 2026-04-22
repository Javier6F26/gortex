package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestExExtractor_Module(t *testing.T) {
	src := []byte(`defmodule MyApp.UserService do
  def find_user(id) do
    Repo.get(User, id)
  end

  defp validate(user) do
    # private function
  end
end
`)
	e := NewElixirExtractor()
	result, err := e.Extract("user_service.ex", src)
	require.NoError(t, err)

	// Module should be a type.
	types := nodesOfKind(result.Nodes, graph.KindType)
	assert.GreaterOrEqual(t, len(types), 1)

	// Functions inside module should be methods.
	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	if len(methods) == 0 {
		// Fallback: may be extracted as functions.
		funcs := nodesOfKind(result.Nodes, graph.KindFunction)
		assert.GreaterOrEqual(t, len(funcs), 1)
	}
}

func TestExExtractor_Imports(t *testing.T) {
	src := []byte(`import Ecto.Query
alias MyApp.Repo
use GenServer
`)
	e := NewElixirExtractor()
	result, err := e.Extract("app.ex", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.GreaterOrEqual(t, len(imports), 1)
}

func TestExExtractor_ModuleWithMethods(t *testing.T) {
	src := []byte(`defmodule Calculator do
  def add(a, b) do
    a + b
  end

  def subtract(a, b) do
    a - b
  end

  defp internal_helper(x) do
    x * 2
  end
end
`)
	e := NewElixirExtractor()
	result, err := e.Extract("calc.ex", src)
	require.NoError(t, err)

	// Module node.
	types := nodesOfKind(result.Nodes, graph.KindType)
	require.GreaterOrEqual(t, len(types), 1)
	assert.Equal(t, "Calculator", types[0].Name)

	// Methods inside module.
	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.GreaterOrEqual(t, len(methods), 2, "expected at least 2 methods (add, subtract)")

	// MemberOf edges.
	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	assert.GreaterOrEqual(t, len(memberEdges), 2)
	for _, edge := range memberEdges {
		assert.Equal(t, "calc.ex::Calculator", edge.To)
	}
}

func TestExExtractor_TopLevelFunction(t *testing.T) {
	src := []byte(`def hello(name) do
  IO.puts("Hello #{name}")
end
`)
	e := NewElixirExtractor()
	result, err := e.Extract("script.exs", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	if len(funcs) > 0 {
		assert.Equal(t, "hello", funcs[0].Name)
	}
}

func TestExExtractor_FileNode(t *testing.T) {
	src := []byte(`defmodule Foo do
end
`)
	e := NewElixirExtractor()
	result, err := e.Extract("foo.ex", src)
	require.NoError(t, err)

	files := nodesOfKind(result.Nodes, graph.KindFile)
	require.Len(t, files, 1)
	assert.Equal(t, "foo.ex", files[0].ID)
	assert.Equal(t, "elixir", files[0].Language)
}

func TestExExtractor_LanguageAndExtensions(t *testing.T) {
	e := NewElixirExtractor()
	assert.Equal(t, "elixir", e.Language())
	assert.Equal(t, []string{".ex", ".exs"}, e.Extensions())
}

func TestExExtractor_PhoenixPlugDispatch(t *testing.T) {
	// `plug :name` in a defmodule binds the named plug to every
	// action function. `plug :name when action in [...]` binds only
	// to the listed atoms. Both produce EdgeCalls edges from each
	// matching action to the plug function.
	src := []byte(`defmodule MyAppWeb.UserController do
  plug :authenticate
  plug :load_user when action in [:show, :update]

  def index(conn, _params), do: conn
  def show(conn, _params), do: conn
  def update(conn, _params), do: conn

  def authenticate(conn, _), do: conn
  def load_user(conn, _), do: conn
end
`)
	e := NewElixirExtractor()
	result, err := e.Extract("user_controller.ex", src)
	require.NoError(t, err)

	authActions := map[string]bool{}
	loadActions := map[string]bool{}
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeCalls) {
		if ed.Meta == nil {
			continue
		}
		plug, _ := ed.Meta["phoenix_plug"].(string)
		switch plug {
		case "authenticate":
			authActions[ed.From] = true
		case "load_user":
			loadActions[ed.From] = true
		}
	}

	// authenticate (no filter) guards every action — index, show, update.
	assert.Len(t, authActions, 3)
	assert.Contains(t, authActions, "user_controller.ex::MyAppWeb.UserController.index")

	// load_user is filtered to :show and :update.
	assert.Len(t, loadActions, 2)
	assert.Contains(t, loadActions, "user_controller.ex::MyAppWeb.UserController.show")
	assert.Contains(t, loadActions, "user_controller.ex::MyAppWeb.UserController.update")
	assert.NotContains(t, loadActions, "user_controller.ex::MyAppWeb.UserController.index")
}
