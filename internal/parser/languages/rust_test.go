package languages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

// Regression: a bodyless trait-method signature must mint a
// <file>::<Trait>.<name> KindMethod node (marked trait_decl) — the node
// rust-analyzer binds every dispatch call site to. Parity with PHP's
// extractMethod / C#'s bodyless interface methods.
func TestRsExtractor_BodylessTraitMethodNode(t *testing.T) {
	src := []byte(`trait Sink {
    fn write(&self, msg: &str);
    fn error_message(&self) -> String;
}
struct FileSink;
impl Sink for FileSink {
    fn write(&self, msg: &str) {}
    fn error_message(&self) -> String { String::new() }
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("sink.rs", src)
	require.NoError(t, err)

	byID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		byID[n.ID] = n
	}

	m := byID["sink.rs::Sink.write"]
	require.NotNil(t, m, "bodyless trait method must mint a node")
	assert.Equal(t, graph.KindMethod, m.Kind)
	assert.Equal(t, "Sink", m.Meta["receiver"])
	assert.Equal(t, "true", m.Meta["trait_decl"])

	require.NotNil(t, byID["sink.rs::Sink.error_message"],
		"a bodyless trait method with a return type mints a node too")
}

func TestRsExtractor_Function(t *testing.T) {
	src := []byte(`fn greet(name: &str) -> String {
    format!("Hello {}", name)
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	require.Len(t, funcs, 1)
	assert.Equal(t, "greet", funcs[0].Name)
}

func TestRsExtractor_Struct(t *testing.T) {
	src := []byte(`struct Config {
    port: u16,
    host: String,
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("config.rs", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	require.Len(t, types, 1)
	assert.Equal(t, "Config", types[0].Name)
}

func TestRsExtractor_EnumVariants(t *testing.T) {
	src := []byte(`enum Shape {
    Circle,
    Rectangle(f64, f64),
    Labeled { name: String },
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("shape.rs", src)
	require.NoError(t, err)

	// Enum marked with Meta["kind"]="enum".
	types := nodesOfKind(result.Nodes, graph.KindType)
	require.Len(t, types, 1)
	assert.Equal(t, "Shape", types[0].Name)
	assert.Equal(t, "enum", types[0].Meta["kind"])

	variants := map[string]bool{}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindVariable && n.Meta != nil && n.Meta["kind"] == "enum_variant" {
			variants[n.Name] = true
		}
	}
	assert.Equal(t, map[string]bool{"Circle": true, "Rectangle": true, "Labeled": true}, variants)
}

func TestRsExtractor_StructFields(t *testing.T) {
	src := []byte(`pub struct User {
    pub id: u64,
    pub name: String,
    email: String,
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("user.rs", src)
	require.NoError(t, err)

	fields := map[string]bool{}
	for _, n := range result.Nodes {
		if n.Kind == graph.KindField {
			fields[n.Name] = true
		}
	}
	assert.Equal(t, map[string]bool{"id": true, "name": true, "email": true}, fields)
}

func TestRsExtractor_Trait(t *testing.T) {
	src := []byte(`trait Repository {
    fn find_by_id(&self, id: &str) -> Option<User>;
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("store.rs", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.Equal(t, "Repository", ifaces[0].Name)
}

func TestRsExtractor_ImplMethods(t *testing.T) {
	src := []byte(`struct Server {
    port: u16,
}

impl Server {
    fn new(port: u16) -> Self {
        Server { port }
    }

    fn start(&self) {
        println!("Starting on {}", self.port);
    }
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("server.rs", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	assert.Len(t, methods, 2) // new, start

	// memberEdges now include the struct field (`port`) alongside the
	// two impl methods — 3 total, all pointing at Server.
	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	assert.Len(t, memberEdges, 3)
	for _, e := range memberEdges {
		assert.Equal(t, "server.rs::Server", e.To)
	}

	// Methods should NOT appear as top-level functions.
	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	assert.Len(t, funcs, 0)
}

func TestRsExtractor_ImplMethodMeta(t *testing.T) {
	src := []byte(`struct Foo {}

impl Foo {
    fn bar(&self) {}
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("foo.rs", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.Len(t, methods, 1)
	assert.Equal(t, "bar", methods[0].Name)
	assert.Equal(t, "foo.rs::Foo.bar", methods[0].ID)
	assert.Equal(t, "Foo", methods[0].Meta["receiver"])
}

func TestRsExtractor_TraitMethods(t *testing.T) {
	src := []byte(`trait Repository {
    fn find_by_id(&self, id: &str) -> Option<User>;
    fn save(&mut self, user: User);
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("store.rs", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	methods, ok := ifaces[0].Meta["methods"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"find_by_id", "save"}, methods)
}

func TestRsExtractor_Use(t *testing.T) {
	src := []byte(`use std::collections::HashMap;
use tokio::net::TcpListener;
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	require.Len(t, imports, 2)
}

// TestRsExtractor_PubUseTaggedAsReExport checks that a `pub use`
// (and `pub(crate) use`) declaration tags its import edge as a
// re-export, while a plain private `use` does not — the signal the
// re-export chain follower needs to distinguish a transparent forward
// from a private import.
func TestRsExtractor_PubUseTaggedAsReExport(t *testing.T) {
	src := []byte(`use crate::internal::Private;
pub use crate::api::Public;
pub(crate) use crate::api::Shared;
`)
	e := NewRustExtractor()
	result, err := e.Extract("lib.rs", src)
	require.NoError(t, err)

	reexport := map[string]bool{}
	for _, edge := range edgesOfKind(result.Edges, graph.EdgeImports) {
		isRe := edge.Meta != nil && edge.Meta["reexport"] == true
		reexport[edge.To] = isRe
	}
	require.False(t, reexport["unresolved::import::crate/internal/Private"],
		"a private `use` must not be tagged as a re-export")
	require.True(t, reexport["unresolved::import::crate/api/Public"],
		"`pub use` must be tagged as a re-export")
	require.True(t, reexport["unresolved::import::crate/api/Shared"],
		"`pub(crate) use` must be tagged as a re-export")
}

func TestRsExtractor_TypeEnv_ExplicitType(t *testing.T) {
	src := []byte(`struct Config {
    port: u16,
}

impl Config {
    fn start(&self) {}
}

fn main() {
    let cfg: Config = Config { port: 8080 };
    cfg.start();
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var startCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "start") {
			startCall = c
			break
		}
	}
	require.NotNil(t, startCall, "expected a call edge to start")
	require.NotNil(t, startCall.Meta, "expected Meta on start call edge")
	assert.Equal(t, "Config", startCall.Meta["receiver_type"])
}

func TestRsExtractor_TypeEnv_NewCall(t *testing.T) {
	src := []byte(`struct Server {
    port: u16,
}

impl Server {
    fn new(port: u16) -> Self {
        Server { port }
    }

    fn listen(&self) {}
}

fn main() {
    let srv = Server::new(8080);
    srv.listen();
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var listenCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "listen") {
			listenCall = c
			break
		}
	}
	require.NotNil(t, listenCall)
	require.NotNil(t, listenCall.Meta)
	assert.Equal(t, "Server", listenCall.Meta["receiver_type"])
}

func TestRsExtractor_TypeEnv_Unknown(t *testing.T) {
	src := []byte(`fn get_service() -> Box<dyn std::any::Any> {
    todo!()
}

fn main() {
    let svc = get_service();
    svc.process();
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("main.rs", src)
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
	assert.NotContains(t, processCall.Meta, "receiver_type", "unknown type should not produce a receiver_type hint")
}

func TestRsExtractor_TypeEnv_Chain(t *testing.T) {
	src := []byte(`struct Order {
    id: i32,
}

struct UserService {
    name: String,
}

impl UserService {
    fn get_order(&self) -> Order {
        Order { id: 1 }
    }
}

impl Order {
    fn total(&self) -> i32 {
        42
    }
}

fn main() {
    let svc: UserService = UserService { name: String::new() };
    svc.get_order().total();
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("app.rs", src)
	require.NoError(t, err)

	// Verify return_type is set on get_order method.
	var getOrderNode *graph.Node
	for _, n := range result.Nodes {
		if n.Name == "get_order" {
			getOrderNode = n
			break
		}
	}
	require.NotNil(t, getOrderNode, "expected a node for get_order")
	assert.Equal(t, "Order", getOrderNode.Meta["return_type"])

	// Verify chain resolution: svc.get_order() should resolve to Order.
	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	var totalCall *graph.Edge
	for _, c := range calls {
		if strings.HasSuffix(c.To, "total") {
			totalCall = c
			break
		}
	}
	require.NotNil(t, totalCall, "expected a call edge to total")
	require.NotNil(t, totalCall.Meta, "expected Meta on total call edge")
	assert.Equal(t, "Order", totalCall.Meta["receiver_type"])
}

func TestRsExtractor_DocAndVisibility(t *testing.T) {
	src := []byte(`/// The greeter.
///
/// Used everywhere.
pub fn greet() {}

/// Internal helper.
fn helper() {}

/// Crate-only.
pub(crate) fn restricted() {}

/// The Server type.
pub struct Server {}

/// Trait Foo.
pub trait Foo {}
`)
	e := NewRustExtractor()
	result, err := e.Extract("lib.rs", src)
	require.NoError(t, err)

	byID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		byID[n.ID] = n
	}

	greet := byID["lib.rs::greet"]
	require.NotNil(t, greet)
	if greet.Meta["visibility"] != "public" {
		t.Fatalf("greet.vis = %q", greet.Meta["visibility"])
	}
	if greet.Meta["doc"] != "The greeter." {
		t.Fatalf("greet.doc = %q", greet.Meta["doc"])
	}

	helper := byID["lib.rs::helper"]
	require.NotNil(t, helper)
	if helper.Meta["visibility"] != "private" {
		t.Fatalf("helper.vis = %q", helper.Meta["visibility"])
	}

	restricted := byID["lib.rs::restricted"]
	require.NotNil(t, restricted)
	if restricted.Meta["visibility"] != "internal" {
		t.Fatalf("restricted.vis = %q", restricted.Meta["visibility"])
	}

	server := byID["lib.rs::Server"]
	require.NotNil(t, server)
	if server.Meta["visibility"] != "public" {
		t.Fatalf("Server.vis = %q", server.Meta["visibility"])
	}
	if server.Meta["doc"] != "The Server type." {
		t.Fatalf("Server.doc = %q", server.Meta["doc"])
	}

	foo := byID["lib.rs::Foo"]
	require.NotNil(t, foo)
	if foo.Meta["visibility"] != "public" {
		t.Fatalf("Foo.vis = %q", foo.Meta["visibility"])
	}
	if foo.Meta["doc"] != "Trait Foo." {
		t.Fatalf("Foo.doc = %q", foo.Meta["doc"])
	}
}

func TestRsExtractor_AnnotationEdges(t *testing.T) {
	src := []byte(`#[derive(Debug, Clone)]
#[serde(rename_all = "snake_case")]
pub struct User {
    name: String,
}

#[test]
fn test_user() {}

#[deprecated(note = "use NewThing instead")]
pub fn old_thing() {}
`)
	e := NewRustExtractor()
	result, err := e.Extract("user.rs", src)
	require.NoError(t, err)

	annNames := map[string]bool{}
	for _, n := range result.Nodes {
		if v, _ := n.Meta["kind"].(string); v == "annotation" {
			annNames[n.Name] = true
		}
	}
	for _, want := range []string{"Debug", "Clone", "serde", "test", "deprecated"} {
		if !annNames[want] {
			t.Fatalf("missing annotation node %q (got %v)", want, annNames)
		}
	}

	edges := map[string][]string{}
	for _, e := range result.Edges {
		if e.Kind != graph.EdgeAnnotated {
			continue
		}
		edges[e.From] = append(edges[e.From], e.To)
	}

	userID := "user.rs::User"
	if !rustTestContains(edges[userID], "annotation::rust::Debug") {
		t.Fatalf("missing Debug derive on User, got %v", edges[userID])
	}
	if !rustTestContains(edges[userID], "annotation::rust::Clone") {
		t.Fatalf("missing Clone derive on User, got %v", edges[userID])
	}
	if !rustTestContains(edges[userID], "annotation::rust::serde") {
		t.Fatalf("missing serde on User, got %v", edges[userID])
	}

	testFnID := "user.rs::test_user"
	if !rustTestContains(edges[testFnID], "annotation::rust::test") {
		t.Fatalf("missing #[test] on test_user, got %v", edges[testFnID])
	}
}

func rustTestContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func TestRsExtractor_ThrowsResult(t *testing.T) {
	src := []byte(`pub fn parse(s: &str) -> Result<i32, ParseError> { Ok(0) }

pub fn open() -> Result<File, std::io::Error> { unimplemented!() }

pub fn boxed() -> Result<u8, Box<dyn std::error::Error>> { Ok(0) }

pub fn no_error() -> i32 { 0 }
`)
	e := NewRustExtractor()
	result, err := e.Extract("lib.rs", src)
	require.NoError(t, err)

	throws := edgesOfKind(result.Edges, graph.EdgeThrows)
	got := map[string]string{}
	for _, e := range throws {
		got[e.From] = e.To
	}
	assert.Equal(t, "unresolved::ParseError", got["lib.rs::parse"])
	assert.Equal(t, "unresolved::Error", got["lib.rs::open"], "trailing ident of std::io::Error")
	assert.Equal(t, "unresolved::Error", got["lib.rs::boxed"], "Box<dyn Error> should land on Error")

	if _, ok := got["lib.rs::no_error"]; ok {
		t.Fatalf("no_error has no Result return; should not throw")
	}
}

func TestRsExtractor_GenericTypeParams(t *testing.T) {
	src := []byte(`pub fn collect<T: Clone, U>(items: Vec<T>, mapper: impl Fn(T) -> U) -> Vec<U> {
    items.into_iter().map(mapper).collect()
}
`)
	e := NewRustExtractor()
	result, err := e.Extract("lib.rs", src)
	require.NoError(t, err)

	var collect *graph.Node
	for _, n := range result.Nodes {
		if n.Name == "collect" && n.Kind == graph.KindFunction {
			collect = n
		}
	}
	require.NotNil(t, collect)
	tp, _ := collect.Meta["type_params"].([]map[string]string)
	require.Len(t, tp, 2)
	assert.Equal(t, "T", tp[0]["name"])
	assert.Equal(t, "Clone", tp[0]["bound"])
	assert.Equal(t, "U", tp[1]["name"])
}
