package mcp

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// TestRouteMethodAndPath_NestedContractMeta guards the fix for the
// read/write key mismatch: contract nodes carry route fields under the
// nested contract_meta map, not at the top level where the reader looked.
func TestRouteMethodAndPath_NestedContractMeta(t *testing.T) {
	http := &graph.Node{Kind: graph.KindContract, Meta: map[string]any{
		"type": "http", "role": "provider",
		"contract_meta": map[string]any{"method": "GET", "path": "/v1/users"},
	}}
	if m, p := routeMethodAndPath(http); m != "GET" || p != "/v1/users" {
		t.Fatalf("nested http: got (%q,%q), want (GET,/v1/users)", m, p)
	}

	// gRPC service falls through the method/path branch (the existing
	// short-circuit returns method first); a service-only node exercises
	// the service branch reading from the nested map.
	grpc := &graph.Node{Kind: graph.KindContract, Meta: map[string]any{
		"contract_meta": map[string]any{"service": "UserSvc"},
	}}
	if m, p := routeMethodAndPath(grpc); m != "" || p != "UserSvc" {
		t.Fatalf("nested grpc service: got (%q,%q), want (\"\",UserSvc)", m, p)
	}

	// A node that stamps the fields at the top level still resolves.
	top := &graph.Node{Kind: graph.KindContract, Meta: map[string]any{
		"method": "POST", "path": "/x",
	}}
	if m, p := routeMethodAndPath(top); m != "POST" || p != "/x" {
		t.Fatalf("top-level fallback: got (%q,%q), want (POST,/x)", m, p)
	}
}
