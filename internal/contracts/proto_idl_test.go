package contracts

import "testing"

// TestGRPCExtractor_ProtoProvider_PackageAndCanonical covers the
// IDL-aware provider extraction: the proto package declaration rides
// on Meta["package"] and the fully-qualified canonical method name
// (`<package>.<Service>/<Method>` — the on-wire gRPC identity) on
// Meta["canonical"], while the contract ID stays package-free so
// exact-ID pairing against bare-named stub consumers keeps working.
func TestGRPCExtractor_ProtoProvider_PackageAndCanonical(t *testing.T) {
	ext := &GRPCExtractor{}
	src := []byte(`
syntax = "proto3";

package billing.v1;

service UserService {
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {}
  rpc WatchUsers(WatchUsersRequest) returns (stream UserEvent) {}
}
`)
	out := ext.Extract("user.proto", src, nil, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 contracts, got %d: %+v", len(out), out)
	}

	get := out[0]
	assertContract(t, get, "grpc::UserService::GetUser", ContractGRPC, RoleProvider)
	if get.Meta["package"] != "billing.v1" {
		t.Errorf("Meta[package] = %v, want billing.v1", get.Meta["package"])
	}
	if get.Meta["canonical"] != "billing.v1.UserService/GetUser" {
		t.Errorf("Meta[canonical] = %v, want billing.v1.UserService/GetUser", get.Meta["canonical"])
	}
	if get.Meta["service"] != "UserService" || get.Meta["method"] != "GetUser" {
		t.Errorf("service/method meta = %v / %v", get.Meta["service"], get.Meta["method"])
	}
	if get.Meta["request_type"] != "GetUserRequest" || get.Meta["response_type"] != "GetUserResponse" {
		t.Errorf("request/response meta = %v / %v", get.Meta["request_type"], get.Meta["response_type"])
	}

	watch := out[1]
	assertContract(t, watch, "grpc::UserService::WatchUsers", ContractGRPC, RoleProvider)
	if watch.Meta["response_stream"] != true {
		t.Errorf("Meta[response_stream] = %v, want true", watch.Meta["response_stream"])
	}
	if watch.Meta["canonical"] != "billing.v1.UserService/WatchUsers" {
		t.Errorf("Meta[canonical] = %v", watch.Meta["canonical"])
	}
}

// TestGRPCExtractor_ProtoProvider_NoPackage: without a package
// declaration the canonical name degrades to `<Service>/<Method>` and
// Meta["package"] is absent.
func TestGRPCExtractor_ProtoProvider_NoPackage(t *testing.T) {
	ext := &GRPCExtractor{}
	src := []byte(`
service Pinger {
  rpc Ping(PingRequest) returns (PingResponse);
}
`)
	out := ext.Extract("ping.proto", src, nil, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 contract, got %d", len(out))
	}
	if _, has := out[0].Meta["package"]; has {
		t.Errorf("Meta[package] should be absent, got %v", out[0].Meta["package"])
	}
	if out[0].Meta["canonical"] != "Pinger/Ping" {
		t.Errorf("Meta[canonical] = %v, want Pinger/Ping", out[0].Meta["canonical"])
	}
}

// TestGRPCExtractor_ProtoProvider_MultipleServicesBounded guards the
// brace-bounded service scan: a file declaring two services must not
// attribute the second service's RPCs to the first (the open-ended
// scan emitted OrderService's methods under UserService too).
func TestGRPCExtractor_ProtoProvider_MultipleServicesBounded(t *testing.T) {
	ext := &GRPCExtractor{}
	src := []byte(`
syntax = "proto3";
package shop.v1;

service UserService {
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
}

service OrderService {
  rpc PlaceOrder(PlaceOrderRequest) returns (PlaceOrderResponse);
  rpc CancelOrder(CancelOrderRequest) returns (CancelOrderResponse);
}
`)
	out := ext.Extract("shop.proto", src, nil, nil)
	if len(out) != 3 {
		t.Fatalf("expected 3 contracts (1 + 2), got %d: %+v", len(out), out)
	}
	got := map[string]bool{}
	for _, c := range out {
		got[c.ID] = true
	}
	for _, want := range []string{
		"grpc::UserService::GetUser",
		"grpc::OrderService::PlaceOrder",
		"grpc::OrderService::CancelOrder",
	} {
		if !got[want] {
			t.Errorf("missing contract %s; got %v", want, got)
		}
	}
	if got["grpc::UserService::PlaceOrder"] || got["grpc::UserService::CancelOrder"] {
		t.Errorf("OrderService RPCs leaked into UserService: %v", got)
	}
}

// TestGRPCExtractor_GoServerRegistration covers the code-side provider
// anchor: a `pb.Register<Service>Server(...)` call emits one service-
// level provider contract bound to the enclosing function.
func TestGRPCExtractor_GoServerRegistration(t *testing.T) {
	ext := &GRPCExtractor{}
	src := []byte(`package main

import (
	"google.golang.org/grpc"

	pb "example.com/gen/users"
)

func main() {
	s := grpc.NewServer()
	pb.RegisterUserServiceServer(s, &userServer{})
	s.Serve(lis)
}
`)
	nodes := makeNodes("main.go", []struct {
		name       string
		start, end int
	}{
		{"main", 9, 13},
	})

	out := ext.Extract("main.go", src, nodes, nil)
	var reg *Contract
	for i := range out {
		if out[i].Role == RoleProvider && out[i].ID == "grpc::UserService" {
			reg = &out[i]
		}
	}
	if reg == nil {
		t.Fatalf("missing registration provider contract grpc::UserService; got %+v", out)
	}
	if reg.SymbolID != "main.go::main" {
		t.Errorf("SymbolID = %q, want main.go::main", reg.SymbolID)
	}
	if reg.Meta["registration"] != true {
		t.Errorf("Meta[registration] = %v, want true", reg.Meta["registration"])
	}
	if reg.Meta["service"] != "UserService" {
		t.Errorf("Meta[service] = %v, want UserService", reg.Meta["service"])
	}
}

// TestGRPCExtractor_GoServerRegistration_NotDuplicated: a file with
// neither client constructions nor registrations stays contract-free
// even when it mentions Register-prefixed identifiers without the
// generated-server shape.
func TestGRPCExtractor_GoServerRegistration_NoFalsePositive(t *testing.T) {
	ext := &GRPCExtractor{}
	src := []byte(`package main

func main() {
	registry.RegisterAll(handlers)
	prometheus.MustRegister(collector)
}
`)
	out := ext.Extract("main.go", src, nil, nil)
	for _, c := range out {
		if c.Type == ContractGRPC {
			t.Errorf("unexpected gRPC contract from non-gRPC Register call: %+v", c)
		}
	}
}
