package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func makeNodes(filePath string, fns []struct{ name string; start, end int }) []*graph.Node {
	var nodes []*graph.Node
	for _, f := range fns {
		nodes = append(nodes, &graph.Node{
			ID:        filePath + "::" + f.name,
			Kind:      graph.KindFunction,
			Name:      f.name,
			FilePath:  filePath,
			StartLine: f.start,
			EndLine:   f.end,
		})
	}
	return nodes
}

// TestHTTPExtractor_Go_Gin_HandlerResolution exercises the T1.3 path: when
// the pattern captures the handler identifier (e.g. "listUsers" in
// r.GET("/users", listUsers)) AND that identifier resolves to a function
// node in the same file, the Contract's SymbolID is the handler — not the
// enclosing setupRoutes function. Cross-service traversals landing on the
// provider side then reach business logic, not the router glue.
//
// When the handler doesn't resolve (e.g. lambda, method expr, different
// file) the code falls back to enclosing-symbol behavior — covered by
// TestHTTPExtractor_Go_Gin above, which deliberately omits handler nodes.
func TestHTTPExtractor_Go_Gin_HandlerResolution(t *testing.T) {
	src := []byte(`package main

import "github.com/gin-gonic/gin"

func setupRoutes(r *gin.Engine) {
	r.GET("/api/users", listUsers)
	r.POST("/api/users", createUser)
}

func listUsers()  {}
func createUser() {}
`)
	nodes := makeNodes("main.go", []struct {
		name       string
		start, end int
	}{
		{"setupRoutes", 5, 8},
		{"listUsers", 10, 10},
		{"createUser", 11, 11},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("main.go", src, nodes, nil)

	byPath := map[string]Contract{}
	for _, c := range contracts {
		byPath[c.ID] = c
	}

	get := byPath["http::GET::/api/users"]
	if get.SymbolID != "main.go::listUsers" {
		t.Errorf("GET handler: expected SymbolID=main.go::listUsers (handler), got %q", get.SymbolID)
	}

	post := byPath["http::POST::/api/users"]
	if post.SymbolID != "main.go::createUser" {
		t.Errorf("POST handler: expected SymbolID=main.go::createUser (handler), got %q", post.SymbolID)
	}
}

func TestHTTPExtractor_Go_Gin(t *testing.T) {
	src := []byte(`package main

import "github.com/gin-gonic/gin"

func setupRoutes(r *gin.Engine) {
	r.GET("/api/users", listUsers)
	r.POST("/api/users", createUser)
	r.GET("/api/users/:id", getUser)
	r.DELETE("/api/users/:id", deleteUser)
}
`)
	nodes := makeNodes("main.go", []struct{ name string; start, end int }{
		{"setupRoutes", 5, 10},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("main.go", src, nodes, nil)

	if len(contracts) != 4 {
		t.Fatalf("expected 4 contracts, got %d", len(contracts))
	}

	// Check first contract.
	c := contracts[0]
	if c.Type != ContractHTTP {
		t.Errorf("expected type http, got %s", c.Type)
	}
	if c.Role != RoleProvider {
		t.Errorf("expected role provider, got %s", c.Role)
	}
	if c.Meta["method"] != "GET" {
		t.Errorf("expected method GET, got %s", c.Meta["method"])
	}
	if c.Meta["path"] != "/api/users" {
		t.Errorf("expected path /api/users, got %s", c.Meta["path"])
	}
	if c.ID != "http::GET::/api/users" {
		t.Errorf("expected ID http::GET::/api/users, got %s", c.ID)
	}
	if c.SymbolID != "main.go::setupRoutes" {
		t.Errorf("expected symbol main.go::setupRoutes, got %s", c.SymbolID)
	}
	if c.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %f", c.Confidence)
	}

	// Check path param normalisation.
	c3 := contracts[2]
	if c3.Meta["path"] != "/api/users/{id}" {
		t.Errorf("expected normalised path /api/users/{id}, got %s", c3.Meta["path"])
	}
}

func TestHTTPExtractor_TypeScript_Express(t *testing.T) {
	src := []byte(`
import express from 'express';
const app = express();

function registerRoutes() {
  app.get('/api/products', listProducts);
  app.post('/api/products', createProduct);
  app.get('/api/products/:id', getProduct);
}
`)
	nodes := makeNodes("routes.ts", []struct{ name string; start, end int }{
		{"registerRoutes", 5, 9},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("routes.ts", src, nodes, nil)

	if len(contracts) != 3 {
		t.Fatalf("expected 3 contracts, got %d", len(contracts))
	}

	c := contracts[0]
	if c.Meta["framework"] != "express" {
		t.Errorf("expected framework express, got %s", c.Meta["framework"])
	}
	if c.Meta["method"] != "GET" {
		t.Errorf("expected method GET, got %s", c.Meta["method"])
	}
	if c.Meta["path"] != "/api/products" {
		t.Errorf("expected path /api/products, got %s", c.Meta["path"])
	}
}

func TestHTTPExtractor_Python_FastAPI(t *testing.T) {
	src := []byte(`
from fastapi import FastAPI
app = FastAPI()

@app.get("/api/items")
def list_items():
    return []

@app.post("/api/items")
def create_item(item: Item):
    return item

@app.get("/api/items/{item_id}")
def get_item(item_id: int):
    return item_id
`)
	nodes := makeNodes("main.py", []struct{ name string; start, end int }{
		{"list_items", 6, 7},
		{"create_item", 10, 11},
		{"get_item", 14, 15},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("main.py", src, nodes, nil)

	if len(contracts) != 3 {
		t.Fatalf("expected 3 contracts, got %d", len(contracts))
	}

	if contracts[0].Meta["method"] != "GET" {
		t.Errorf("expected GET, got %s", contracts[0].Meta["method"])
	}
	if contracts[0].Meta["path"] != "/api/items" {
		t.Errorf("expected /api/items, got %s", contracts[0].Meta["path"])
	}
	if contracts[0].Meta["framework"] != "fastapi/flask" {
		t.Errorf("expected fastapi/flask, got %s", contracts[0].Meta["framework"])
	}
}

func TestHTTPExtractor_Java_Spring(t *testing.T) {
	src := []byte(`
@RestController
@RequestMapping("/api")
public class UserController {

    @GetMapping("/users")
    public List<User> listUsers() {
        return userService.findAll();
    }

    @PostMapping("/users")
    public User createUser(@RequestBody User user) {
        return userService.save(user);
    }

    @DeleteMapping("/users/{id}")
    public void deleteUser(@PathVariable Long id) {
        userService.delete(id);
    }
}
`)
	nodes := makeNodes("UserController.java", []struct{ name string; start, end int }{
		{"listUsers", 6, 8},
		{"createUser", 11, 13},
		{"deleteUser", 16, 18},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("UserController.java", src, nodes, nil)

	// Should detect @RequestMapping + @GetMapping + @PostMapping + @DeleteMapping
	if len(contracts) < 3 {
		t.Fatalf("expected at least 3 contracts, got %d", len(contracts))
	}

	// Find the GetMapping contract.
	found := false
	for _, c := range contracts {
		if c.Meta["method"] == "GET" && c.Meta["path"] == "/users" {
			found = true
			if c.Meta["framework"] != "spring" {
				t.Errorf("expected spring framework, got %s", c.Meta["framework"])
			}
		}
	}
	if !found {
		t.Error("did not find GET /users contract from @GetMapping")
	}
}

func TestHTTPExtractor_Consumers(t *testing.T) {
	src := []byte(`package main

import "net/http"

func callAPI() {
	http.Get("http://service-b/api/users")
	http.NewRequest("POST", "http://service-b/api/orders", nil)
}
`)
	nodes := makeNodes("client.go", []struct{ name string; start, end int }{
		{"callAPI", 5, 8},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("client.go", src, nodes, nil)

	if len(contracts) != 2 {
		t.Fatalf("expected 2 contracts, got %d", len(contracts))
	}

	for _, c := range contracts {
		if c.Role != RoleConsumer {
			t.Errorf("expected consumer role, got %s", c.Role)
		}
	}
}

func TestHTTPExtractor_JS_Consumers(t *testing.T) {
	src := []byte(`
async function fetchData() {
  const users = await fetch('/api/users');
  const result = await axios.post('/api/orders', data);
}
`)
	nodes := makeNodes("api.js", []struct{ name string; start, end int }{
		{"fetchData", 2, 5},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("api.js", src, nodes, nil)

	if len(contracts) != 2 {
		t.Fatalf("expected 2 contracts, got %d", len(contracts))
	}

	// fetch is confidence 0.7, axios is 0.9
	for _, c := range contracts {
		if c.Role != RoleConsumer {
			t.Errorf("expected consumer, got %s", c.Role)
		}
	}
}

func TestHTTPExtractor_Python_Consumers(t *testing.T) {
	src := []byte(`
import requests

def call_service():
    resp = requests.get("http://service/api/users")
    resp2 = requests.post("http://service/api/orders")
`)
	nodes := makeNodes("client.py", []struct{ name string; start, end int }{
		{"call_service", 4, 6},
	})

	ext := &HTTPExtractor{}
	contracts := ext.Extract("client.py", src, nodes, nil)

	if len(contracts) != 2 {
		t.Fatalf("expected 2 contracts, got %d", len(contracts))
	}
}

func TestNormalizeHTTPPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/users/:id", "/users/{id}"},
		{"/users/<int:id>", "/users/{id}"},
		{"/users/{id}", "/users/{id}"},
		{`"/api/v1/items/"`, "/api/v1/items"},
		{"api/users", "/api/users"},
		{"/", "/"},
	}

	for _, tt := range tests {
		got := NormalizeHTTPPath(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeHTTPPath(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestHTTPExtractor_SupportedLanguages(t *testing.T) {
	ext := &HTTPExtractor{}
	langs := ext.SupportedLanguages()
	if len(langs) != 5 {
		t.Errorf("expected 5 languages, got %d", len(langs))
	}
}

func TestHTTPExtractor_NoSymbol(t *testing.T) {
	// When there are no enclosing functions, SymbolID should be empty.
	src := []byte(`r.GET("/api/test", handler)`)
	contracts := (&HTTPExtractor{}).Extract("main.go", src, nil, nil)
	if len(contracts) != 1 {
		t.Fatalf("expected 1 contract, got %d", len(contracts))
	}
	if contracts[0].SymbolID != "" {
		t.Errorf("expected empty symbol ID, got %s", contracts[0].SymbolID)
	}
}
