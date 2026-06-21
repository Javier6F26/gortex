package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func cppTypedAsTo(edges []*graph.Edge) map[string]bool {
	out := map[string]bool{}
	for _, e := range edges {
		if e.Kind == graph.EdgeTypedAs {
			out[e.To] = true
		}
	}
	return out
}

func TestCppExtractor_TypeUse_LocalVariable(t *testing.T) {
	src := []byte(`HttpResponse handle() {
    HttpResponse resp = get();
    int count = 0;
    return resp;
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("handler.cpp", src)
	require.NoError(t, err)

	typed := edgesOfKind(result.Edges, graph.EdgeTypedAs)
	to := cppTypedAsTo(result.Edges)

	// A local declaration `HttpResponse resp = get();` references HttpResponse.
	assert.True(t, to["unresolved::HttpResponse"],
		"expected EdgeTypedAs to unresolved::HttpResponse, got %v", to)
	// `int count` is a primitive — no edge.
	assert.False(t, to["unresolved::int"], "primitive int must not produce a type-use edge")

	// Every type-use edge must ride at OriginASTInferred and originate
	// from the enclosing function node, never the file node.
	for _, edge := range typed {
		assert.Equal(t, graph.OriginASTInferred, edge.Origin)
		assert.NotEqual(t, "handler.cpp", edge.From, "type-use edge must attribute to the function, not the file")
	}
}

func TestCppExtractor_TypeUse_PointerAndReference(t *testing.T) {
	src := []byte(`void run() {
    Foo* p = nullptr;
    Bar& r = make();
    const Baz& cb = get();
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("ptr.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::Foo"], "pointer `Foo* p` references Foo: %v", to)
	assert.True(t, to["unresolved::Bar"], "reference `Bar& r` references Bar: %v", to)
	assert.True(t, to["unresolved::Baz"], "const-reference `const Baz&` references Baz: %v", to)
}

func TestCppExtractor_TypeUse_SmartPointerAndContainer(t *testing.T) {
	src := []byte(`void wire() {
    std::shared_ptr<Service> svc = makeService();
    std::unique_ptr<Connection> conn;
    std::vector<Widget> widgets;
    std::optional<Result> maybe;
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("wire.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::Service"], "shared_ptr<Service> unwraps to Service: %v", to)
	assert.True(t, to["unresolved::Connection"], "unique_ptr<Connection> unwraps to Connection: %v", to)
	assert.True(t, to["unresolved::Widget"], "vector<Widget> unwraps to Widget: %v", to)
	assert.True(t, to["unresolved::Result"], "optional<Result> unwraps to Result: %v", to)

	// The wrapper names themselves must not appear as targets.
	assert.False(t, to["unresolved::shared_ptr"], "wrapper name must not be a target")
	assert.False(t, to["unresolved::vector"], "wrapper name must not be a target")
}

func TestCppExtractor_TypeUse_NamespaceQualified(t *testing.T) {
	src := []byte(`void f() {
    ns::Baz b;
    a::b::Deep d;
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("ns.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::Baz"], "ns::Baz strips to Baz: %v", to)
	assert.True(t, to["unresolved::Deep"], "a::b::Deep strips to Deep: %v", to)
}

func TestCppExtractor_TypeUse_Parameters(t *testing.T) {
	src := []byte(`void process(Request req, const std::string& name, Config cfg, int n) {
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("params.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::Request"], "parameter type Request: %v", to)
	assert.True(t, to["unresolved::Config"], "parameter type Config: %v", to)
	// std::string is a stdlib primitive alias — skipped.
	assert.False(t, to["unresolved::string"], "std::string parameter must not produce an edge")
	assert.False(t, to["unresolved::int"], "int parameter must not produce an edge")
}

func TestCppExtractor_TypeUse_ReturnType(t *testing.T) {
	// A by-value return type names the returned type. (Pointer-returning
	// free functions like `Connection* makeConn(...)` are not captured as
	// function nodes by the base extractor's query — a separate pre-existing
	// limitation — so we assert the by-value form here.)
	src := []byte(`Connection makeConn(Config cfg) {
    return Connection();
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("ret.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::Connection"], "return type Connection references Connection: %v", to)
	assert.True(t, to["unresolved::Config"], "parameter type Config: %v", to)
}

func TestCppExtractor_TypeUse_Fields(t *testing.T) {
	src := []byte(`class Widget {
public:
    HttpResponse resp;
    std::shared_ptr<Service> svc;
    int count;
};

struct Bundle {
    Payload payload;
    double weight;
};
`)
	e := NewCppExtractor()
	result, err := e.Extract("widget.cpp", src)
	require.NoError(t, err)

	to := cppTypedAsTo(result.Edges)
	assert.True(t, to["unresolved::HttpResponse"], "class field type HttpResponse: %v", to)
	assert.True(t, to["unresolved::Service"], "class field shared_ptr<Service> -> Service: %v", to)
	assert.True(t, to["unresolved::Payload"], "struct field type Payload: %v", to)
	// Primitives skipped.
	assert.False(t, to["unresolved::int"], "int field must not produce an edge")
	assert.False(t, to["unresolved::double"], "double field must not produce an edge")

	// Field edges must originate from the owning class/struct node.
	for _, edge := range result.Edges {
		if edge.Kind != graph.EdgeTypedAs {
			continue
		}
		if edge.To == "unresolved::HttpResponse" || edge.To == "unresolved::Service" {
			assert.Equal(t, "widget.cpp::Widget", edge.From, "class field type-use owner")
		}
		if edge.To == "unresolved::Payload" {
			assert.Equal(t, "widget.cpp::Bundle", edge.From, "struct field type-use owner")
		}
	}
}

func TestCppExtractor_TypeUse_NoDuplicateEdges(t *testing.T) {
	// Same type used three times in one function must yield a single edge.
	src := []byte(`void f() {
    Foo a;
    Foo b;
    Foo c = make();
}
`)
	e := NewCppExtractor()
	result, err := e.Extract("dup.cpp", src)
	require.NoError(t, err)

	n := 0
	for _, edge := range result.Edges {
		if edge.Kind == graph.EdgeTypedAs && edge.To == "unresolved::Foo" {
			n++
		}
	}
	assert.Equal(t, 1, n, "repeated local type must produce exactly one type-use edge")
}

func TestCanonicalizeCppTypeRef(t *testing.T) {
	cases := map[string]string{
		"HttpResponse":                "HttpResponse",
		"Foo*":                        "Foo",
		"Foo **":                      "Foo",
		"Bar&":                        "Bar",
		"Baz&&":                       "Baz",
		"const Foo&":                  "Foo",
		"const std::string&":          "string",
		"std::shared_ptr<Service>":    "Service",
		"std::unique_ptr<Connection>": "Connection",
		"std::vector<Widget>":         "Widget",
		"std::optional<Result>":       "Result",
		"ns::Baz":                     "Baz",
		"a::b::c::Deep":               "Deep",
		"Map<K, V>":                   "Map",
		"int":                         "int",
		"":                            "",
		"  ":                          "",
	}
	for in, want := range cases {
		got := canonicalizeCppTypeRef(in)
		assert.Equal(t, want, got, "canonicalizeCppTypeRef(%q)", in)
	}
}

func TestIsCppPrimitive(t *testing.T) {
	for _, p := range []string{"int", "char", "bool", "float", "double", "void", "long", "short", "unsigned", "size_t", "auto", "string"} {
		assert.True(t, isCppPrimitive(p), "%q should be primitive", p)
	}
	for _, np := range []string{"HttpResponse", "Foo", "Service", "Widget"} {
		assert.False(t, isCppPrimitive(np), "%q should not be primitive", np)
	}
}
