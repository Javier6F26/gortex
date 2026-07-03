package languages

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zzet/gortex/internal/graph"
)

func TestPHPExtractor_Class(t *testing.T) {
	src := []byte(`<?php
class UserService {
    private $db;

    public function __construct($db) {
        $this->db = $db;
    }

    public function findUser($id) {
        return $this->db->query($id);
    }
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("service.php", src)
	require.NoError(t, err)

	types := nodesOfKind(result.Nodes, graph.KindType)
	assert.GreaterOrEqual(t, len(types), 1)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	assert.GreaterOrEqual(t, len(methods), 1)
}

func TestPHPExtractor_ScopeInterfaces(t *testing.T) {
	src := []byte(`<?php
interface HandlerInterface {}
interface ProcessableInterface extends HandlerInterface {}
class StreamHandler extends AbstractHandler implements HandlerInterface, ProcessableInterface {
    public function handle(): bool { return true; }
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("h.php", src)
	require.NoError(t, err)

	byName := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		byName[n.Name] = n
	}

	cls := byName["StreamHandler"]
	require.NotNil(t, cls)
	assert.Equal(t, "AbstractHandler", cls.Meta["scope_parent"], "extends → scope_parent")
	ifaces, _ := cls.Meta["scope_interfaces"].(string)
	assert.Contains(t, ifaces, "HandlerInterface", "implements → scope_interfaces")
	assert.Contains(t, ifaces, "ProcessableInterface", "implements → scope_interfaces")

	pi := byName["ProcessableInterface"]
	require.NotNil(t, pi)
	pifaces, _ := pi.Meta["scope_interfaces"].(string)
	assert.Contains(t, pifaces, "HandlerInterface", "interface extends → scope_interfaces")
}

func TestPHPExtractor_Function(t *testing.T) {
	src := []byte(`<?php
function greet($name) {
    echo "Hello $name";
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("app.php", src)
	require.NoError(t, err)

	funcs := nodesOfKind(result.Nodes, graph.KindFunction)
	assert.GreaterOrEqual(t, len(funcs), 1)
}

func TestPHPExtractor_Interface(t *testing.T) {
	src := []byte(`<?php
interface Repository {
    public function findById($id);
    public function save($entity);
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("repo.php", src)
	require.NoError(t, err)

	ifaces := nodesOfKind(result.Nodes, graph.KindInterface)
	require.Len(t, ifaces, 1)
	assert.Equal(t, "Repository", ifaces[0].Name)
}

func TestPHPExtractor_MethodMemberOf(t *testing.T) {
	src := []byte(`<?php
class UserService {
    public function findUser($id) {
        return $this->db->query($id);
    }
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("service.php", src)
	require.NoError(t, err)

	methods := nodesOfKind(result.Nodes, graph.KindMethod)
	require.GreaterOrEqual(t, len(methods), 1)

	memberEdges := edgesOfKind(result.Edges, graph.EdgeMemberOf)
	require.GreaterOrEqual(t, len(memberEdges), 1)
	for _, e := range memberEdges {
		assert.Equal(t, "service.php::UserService", e.To)
	}
}

func TestPHPExtractor_Namespace(t *testing.T) {
	src := []byte(`<?php
namespace App\Models;

class User {}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("user.php", src)
	require.NoError(t, err)

	pkgs := nodesOfKind(result.Nodes, graph.KindPackage)
	require.GreaterOrEqual(t, len(pkgs), 1)
}

func TestPHPExtractor_UseImport(t *testing.T) {
	src := []byte(`<?php
use App\Models\User;
use App\Services\UserService;

class Controller {}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("controller.php", src)
	require.NoError(t, err)

	imports := edgesOfKind(result.Edges, graph.EdgeImports)
	assert.GreaterOrEqual(t, len(imports), 2)
}

func TestPHPExtractor_CallSites(t *testing.T) {
	src := []byte(`<?php
class Service {
    public function run() {
        $this->helper();
        doSomething();
    }
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("svc.php", src)
	require.NoError(t, err)

	calls := edgesOfKind(result.Edges, graph.EdgeCalls)
	assert.GreaterOrEqual(t, len(calls), 1)
}

func TestPHPExtractor_LaravelControllerMiddleware(t *testing.T) {
	// $this->middleware(X::class) in the controller ctor binds X.handle
	// as a dispatch edge on every action. ->only([...]) restricts.
	src := []byte(`<?php
class UserController {
    public function __construct() {
        $this->middleware(AuthMiddleware::class);
        $this->middleware(AdminMiddleware::class)->only(['destroy']);
    }
    public function index() {}
    public function show() {}
    public function destroy() {}
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("c.php", src)
	require.NoError(t, err)

	auth := map[string]bool{}
	admin := map[string]bool{}
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeCalls) {
		if ed.Meta == nil {
			continue
		}
		if v, _ := ed.Meta["laravel_middleware"].(string); v == "AuthMiddleware" {
			auth[ed.From] = true
		}
		if v, _ := ed.Meta["laravel_middleware"].(string); v == "AdminMiddleware" {
			admin[ed.From] = true
		}
	}
	assert.Len(t, auth, 3, "AuthMiddleware should bind to every action")
	assert.Len(t, admin, 1, "AdminMiddleware should bind only to :destroy")
	assert.Contains(t, admin, "c.php::UserController.destroy")
}

func TestPHPExtractor_LaravelServiceProviderBindings(t *testing.T) {
	// $this->app->bind(Interface, Impl) emits two EdgeProvides edges:
	// one useClass-style (to Impl with provides_for=Interface) and one
	// token-style (to Interface) so find_usages on the interface
	// surfaces the provider.
	src := []byte(`<?php
class AppServiceProvider {
    public function register() {
        $this->app->bind(Foo::class, FooImpl::class);
        $this->app->singleton(Bar::class, function($app){ return new Bar(); });
    }
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("p.php", src)
	require.NoError(t, err)

	var fooUseClass, fooProvider bool
	var barProvider bool
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeProvides) {
		if ed.Meta == nil {
			continue
		}
		binding, _ := ed.Meta["binding"].(string)
		if binding == "useClass" && ed.Meta["provides_for"] == "Foo" {
			fooUseClass = true
		}
		if binding == "bind" && ed.To == "unresolved::Foo" {
			fooProvider = true
		}
		if binding == "singleton" && ed.To == "unresolved::Bar" {
			barProvider = true
		}
	}
	assert.True(t, fooUseClass, "bind should emit useClass edge to impl")
	assert.True(t, fooProvider, "bind should emit provider edge to interface")
	assert.True(t, barProvider, "singleton with factory should emit provider edge to token")
}

func TestPHPExtractor_SymfonyAsEventListener(t *testing.T) {
	// #[AsEventListener(event: X::class)] on a method emits an
	// EdgeConsumes from the method to X so find_usages(X) returns
	// the listener. Class-level form also supported.
	src := []byte(`<?php
use Symfony\Component\EventDispatcher\Attribute\AsEventListener;

class ClassLevelListener {
    public function __invoke() {}
}

#[AsEventListener(event: UserCreated::class)]
class ClassLevel extends ClassLevelListener {}

class MethodLevel {
    #[AsEventListener(event: UserCreated::class)]
    public function onCreated() {}
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("l.php", src)
	require.NoError(t, err)

	var hasMethod, hasClass bool
	for _, ed := range edgesOfKind(result.Edges, graph.EdgeConsumes) {
		if ed.Meta == nil {
			continue
		}
		attr, _ := ed.Meta["dispatch_attribute"].(string)
		if attr != "AsEventListener" {
			continue
		}
		event, _ := ed.Meta["symfony_event"].(string)
		if event != "UserCreated" {
			continue
		}
		if ed.From == "l.php::MethodLevel.onCreated" {
			hasMethod = true
		}
		if ed.From == "l.php::ClassLevel" {
			hasClass = true
		}
	}
	assert.True(t, hasMethod)
	assert.True(t, hasClass)
}

func TestPHPExtractor_DocAndVisibility(t *testing.T) {
	src := []byte(`<?php

/**
 * Greeter does the thing.
 */
class Greeter {
    /**
     * Says hi.
     */
    public function hello() {}

    private function secret() {}

    protected function helper() {}
}
`)
	e := NewPHPExtractor()
	result, err := e.Extract("Greeter.php", src)
	require.NoError(t, err)

	byID := map[string]*graph.Node{}
	for _, n := range result.Nodes {
		byID[n.ID] = n
	}

	greeter := byID["Greeter.php::Greeter"]
	require.NotNil(t, greeter)
	if greeter.Meta["visibility"] != "public" {
		t.Fatalf("Greeter.vis = %q", greeter.Meta["visibility"])
	}
	if greeter.Meta["doc"] != "Greeter does the thing." {
		t.Fatalf("Greeter.doc = %q", greeter.Meta["doc"])
	}

	hello := byID["Greeter.php::Greeter.hello"]
	require.NotNil(t, hello)
	if hello.Meta["visibility"] != "public" {
		t.Fatalf("hello.vis = %q", hello.Meta["visibility"])
	}
	if hello.Meta["doc"] != "Says hi." {
		t.Fatalf("hello.doc = %q", hello.Meta["doc"])
	}

	secret := byID["Greeter.php::Greeter.secret"]
	require.NotNil(t, secret)
	if secret.Meta["visibility"] != "private" {
		t.Fatalf("secret.vis = %q", secret.Meta["visibility"])
	}

	helper := byID["Greeter.php::Greeter.helper"]
	require.NotNil(t, helper)
	if helper.Meta["visibility"] != "protected" {
		t.Fatalf("helper.vis = %q", helper.Meta["visibility"])
	}
}

// TestPHPExtractor_TraitsEnumsConstsProps is the C4 test: traits, enums with
// cases, class constants, typed properties, method return types, and trait-use
// composition edges are all extracted.
func TestPHPExtractor_TraitsEnumsConstsProps(t *testing.T) {
	src := []byte("<?php\n" +
		"trait Greets { public function hi(): string { return 'hi'; } }\n" +
		"enum Suit: string {\n" +
		"  case Hearts = 'H';\n" +
		"  case Spades = 'S';\n" +
		"  public function color(): string { return 'red'; }\n" +
		"}\n" +
		"class Card {\n" +
		"  use Greets;\n" +
		"  public const MAX = 52;\n" +
		"  private int $rank;\n" +
		"  public function makeCard(): Card { return new Card(); }\n" +
		"}\n")
	res, err := NewPHPExtractor().Extract("c.php", src)
	if err != nil {
		t.Fatal(err)
	}

	byID := map[string]*nodeForCount{}
	for _, n := range res.Nodes {
		byID[n.ID] = &nodeForCount{id: n.ID, name: n.Name}
	}
	kindByName := map[string]graph.NodeKind{}
	rtByName := map[string]string{}
	for _, n := range res.Nodes {
		kindByName[n.Name] = n.Kind
		if n.Meta != nil {
			if rt, _ := n.Meta["return_type"].(string); rt != "" {
				rtByName[n.Name] = rt
			}
		}
	}

	// Trait + enum are types; enum cases are enum members.
	assert.Equal(t, graph.KindType, kindByName["Greets"], "trait Greets is a type")
	assert.Equal(t, graph.KindType, kindByName["Suit"], "enum Suit is a type")
	assert.Equal(t, graph.KindEnumMember, kindByName["Hearts"], "enum case is an enum member")
	assert.Equal(t, graph.KindEnumMember, kindByName["Spades"])
	// Class constant + typed property.
	assert.Equal(t, graph.KindConstant, kindByName["MAX"], "class const MAX")
	assert.Equal(t, graph.KindField, kindByName["rank"], "typed property rank")
	// Method return types (a named return type beats the builtin path).
	assert.Equal(t, "string", rtByName["hi"])
	assert.Equal(t, "Card", rtByName["makeCard"], "non-builtin return type stamped for chaintype")

	// Trait-use composition edge + a non-builtin return edge.
	var sawTraitUse, sawReturnEdge bool
	for _, e := range res.Edges {
		if e.Kind == graph.EdgeExtends && e.From == "c.php::Card" && e.To == "unresolved::Greets" {
			if v, _ := e.Meta["via"].(string); v == "trait" {
				sawTraitUse = true
			}
		}
		if e.Kind == graph.EdgeReturns && e.To == "unresolved::Card" {
			sawReturnEdge = true
		}
	}
	assert.True(t, sawTraitUse, "class should compose the trait via an extends edge")
	assert.True(t, sawReturnEdge, "non-builtin return type should emit a return edge")
}
