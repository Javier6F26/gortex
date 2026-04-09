package languages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestTSExtractor_Function(t *testing.T) {
	src := []byte(`function greet(name: string): string {
  return "Hello " + name;
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	require.Len(t, funcs, 1)
	assert.Equal(t, "greet", funcs[0].Name)
}

func TestTSExtractor_ArrowFunction(t *testing.T) {
	src := []byte(`const handler = () => {
  console.log("hello");
};
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	require.Len(t, funcs, 1)
	assert.Equal(t, "handler", funcs[0].Name)
}

func TestTSExtractor_Class(t *testing.T) {
	src := []byte(`class UserService {
  getUser(id: string) {
    return {};
  }
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("service.ts", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	require.Len(t, types, 1)
	assert.Equal(t, "UserService", types[0].Name)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.Len(t, methods, 1)
	assert.Equal(t, "getUser", methods[0].Name)
}

func TestTSExtractor_Interface(t *testing.T) {
	src := []byte(`interface Config {
  port: number;
  host: string;
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("types.ts", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.Equal(t, "Config", ifaces[0].Name)
}

func TestTSExtractor_Variables(t *testing.T) {
	src := []byte(`const API_URL = "https://api.example.com";
let count = 0;
export const MAX_RETRIES = 3;
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("config.ts", src)
	require.NoError(t, err)

	vars := nodesOfKind(result.Nodes, graph.KindVariable)
	assert.GreaterOrEqual(t, len(vars), 2)
}

func TestTSExtractor_InterfaceMethods(t *testing.T) {
	src := []byte(`interface Repository {
    findById(id: string): User;
    save(user: User): void;
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("repo.ts", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	methods, ok := ifaces[0].Meta["methods"].([]string)
	require.True(t, ok)
	assert.Contains(t, methods, "findById")
	assert.Contains(t, methods, "save")
}

func TestTSExtractor_Imports(t *testing.T) {
	src := []byte(`import { Router } from 'express';
import axios from 'axios';
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	require.Len(t, imports, 2)
}

func TestTSExtractor_TypeEnv_ExplicitType(t *testing.T) {
	src := []byte(`
class UserService {
  save() {}
}

function main() {
  const svc: UserService = new UserService();
  svc.save();
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var saveCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "save") {
			saveCall = c
			break
		}
	}
	require.NotNil(t, saveCall, "expected a call edge to save")
	require.NotNil(t, saveCall.Meta, "expected Meta on save call edge")
	assert.Equal(t, "UserService", saveCall.Meta["receiver_type"])
}

func TestTSExtractor_TypeEnv_NewExpression(t *testing.T) {
	src := []byte(`
class Client {
  connect() {}
}

function main() {
  const client = new Client();
  client.connect();
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var connectCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "connect") {
			connectCall = c
			break
		}
	}
	require.NotNil(t, connectCall)
	require.NotNil(t, connectCall.Meta)
	assert.Equal(t, "Client", connectCall.Meta["receiver_type"])
}

func TestTSExtractor_TypeEnv_Unknown(t *testing.T) {
	src := []byte(`
function getService() { return null; }

function main() {
  const svc = getService();
  svc.process();
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("app.ts", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var processCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "process") {
			processCall = c
			break
		}
	}
	require.NotNil(t, processCall)
	assert.Nil(t, processCall.Meta, "unknown type should not produce Meta")
}

func TestTSExtractor_MethodReceiver(t *testing.T) {
	src := []byte(`
class Server {
  start() {}
  stop() {}
}
`)
	e := NewTypeScriptExtractor()
	result, err := e.Extract("server.ts", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.Len(t, methods, 2)
	for _, m := range methods {
		assert.Equal(t, "Server", m.Meta["receiver"], "method %s should have receiver Server", m.Name)
	}
}
