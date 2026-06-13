package contracts

import "testing"

// TestMatch_RPCCanonicalJoin_MethodCasing: a TS stub call site emits
// camelCase method IDs while the proto IDL declares PascalCase. Exact
// ID pairing misses; the canonical join must pair them.
func TestMatch_RPCCanonicalJoin_MethodCasing(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		FilePath:    "proto/user.proto",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser", "package": "billing.v1"},
	})
	reg.Add(Contract{
		ID:          "grpc::UserService::getUser",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		SymbolID:    "web/api.ts::loadUser",
		FilePath:    "web/api.ts",
		RepoPrefix:  "webapp",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "getUser", "lang": "typescript"},
	})

	result := Match(reg)
	if len(result.Matched) != 1 {
		t.Fatalf("expected 1 canonical-joined match, got %d (orphan providers=%d consumers=%d)",
			len(result.Matched), len(result.OrphanProviders), len(result.OrphanConsumers))
	}
	m := result.Matched[0]
	if m.ContractID != "grpc::UserService::GetUser" {
		t.Errorf("group ID should be the provider's method-level ID, got %s", m.ContractID)
	}
	if !m.CrossRepo {
		t.Error("expected cross-repo join")
	}
	if len(result.OrphanProviders) != 0 || len(result.OrphanConsumers) != 0 {
		t.Errorf("joined contracts must leave the orphan lists: providers=%d consumers=%d",
			len(result.OrphanProviders), len(result.OrphanConsumers))
	}
}

// TestMatch_RPCCanonicalJoin_IDLPlusStub is the IDL↔generated-stub
// scenario end to end at registry level: the .proto definition, a Go
// server registration in the implementing repo, and a Go client stub
// call in a consuming repo must all collapse into one linked group.
func TestMatch_RPCCanonicalJoin_IDLPlusStub(t *testing.T) {
	idl := Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		FilePath:    "proto/user.proto",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser"},
	}
	registration := Contract{
		ID:          "grpc::UserService",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		SymbolID:    "cmd/server/main.go::main",
		FilePath:    "cmd/server/main.go",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "registration": true},
	}
	stubCall := Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		SymbolID:    "client/users.go::fetchUser",
		FilePath:    "client/users.go",
		RepoPrefix:  "gateway",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser", "lang": "go"},
	}

	reg := NewRegistry()
	reg.Add(idl)
	reg.Add(registration)
	reg.Add(stubCall)

	result := Match(reg)

	// Exact pass: IDL provider ↔ stub consumer. Canonical pass: the
	// service-level registration provider joins the same consumer.
	if len(result.Matched) != 2 {
		t.Fatalf("expected 2 links (exact + canonical), got %d: %+v", len(result.Matched), result.Matched)
	}
	groupIDs := map[string]int{}
	providers := map[string]bool{}
	for _, m := range result.Matched {
		groupIDs[m.ContractID]++
		providers[m.Provider.FilePath] = true
		if m.Consumer.SymbolID != "client/users.go::fetchUser" {
			t.Errorf("unexpected consumer: %+v", m.Consumer)
		}
	}
	// Both links group under the method-level contract ID — one
	// bridge group for the RPC.
	if groupIDs["grpc::UserService::GetUser"] != 2 {
		t.Errorf("links should group under the method-level ID: %v", groupIDs)
	}
	if !providers["proto/user.proto"] || !providers["cmd/server/main.go"] {
		t.Errorf("both IDL and registration providers must link: %v", providers)
	}
	if len(result.OrphanProviders) != 0 {
		t.Errorf("registration provider should be joined, orphans: %+v", result.OrphanProviders)
	}
}

// TestMatch_RPCCanonicalJoin_ServiceLevelConsumer: a bare client
// construction (no resolvable method calls) joins every method the
// service provides.
func TestMatch_RPCCanonicalJoin_ServiceLevelConsumer(t *testing.T) {
	reg := NewRegistry()
	for _, method := range []string{"GetUser", "ListUsers"} {
		reg.Add(Contract{
			ID:          "grpc::UserService::" + method,
			Type:        ContractGRPC,
			Role:        RoleProvider,
			FilePath:    "proto/user.proto",
			RepoPrefix:  "svc-users",
			WorkspaceID: "acme",
			ProjectID:   "users",
			Meta:        map[string]any{"service": "UserService", "method": method},
		})
	}
	reg.Add(Contract{
		ID:          "grpc::UserService",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		SymbolID:    "app/client.py::build_stub",
		FilePath:    "app/client.py",
		RepoPrefix:  "py-app",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "lang": "python"},
	})

	result := Match(reg)
	if len(result.Matched) != 2 {
		t.Fatalf("service-level consumer should join both providers, got %d links", len(result.Matched))
	}
	if len(result.OrphanConsumers) != 0 {
		t.Errorf("service-level consumer should be joined: %+v", result.OrphanConsumers)
	}
}

// TestMatch_RPCCanonicalJoin_ThriftProviderGRPCStyleConsumer: thrift
// IDL providers pair with consumers detected through the shared
// generated-stub patterns (typed grpc by the code-side extractor).
func TestMatch_RPCCanonicalJoin_ThriftFamily(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Contract{
		ID:          "thrift::Calculator::add",
		Type:        ContractThrift,
		Role:        RoleProvider,
		FilePath:    "idl/calc.thrift",
		RepoPrefix:  "calc-svc",
		WorkspaceID: "acme",
		ProjectID:   "calc",
		Meta:        map[string]any{"service": "Calculator", "method": "add"},
	})
	reg.Add(Contract{
		ID:          "grpc::Calculator::add",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		SymbolID:    "main.go::compute",
		FilePath:    "main.go",
		RepoPrefix:  "calc-cli",
		WorkspaceID: "acme",
		ProjectID:   "calc",
		Meta:        map[string]any{"service": "Calculator", "method": "add", "lang": "go"},
	})

	result := Match(reg)
	if len(result.Matched) != 1 {
		t.Fatalf("expected thrift/grpc family join, got %d matches", len(result.Matched))
	}
	if result.Matched[0].ContractID != "thrift::Calculator::add" {
		t.Errorf("group ID should be the provider's method-level ID, got %s", result.Matched[0].ContractID)
	}
}

// TestMatch_RPCCanonicalJoin_RespectsBoundary: the canonical join must
// honour the same (workspace, project) boundary as exact matching.
func TestMatch_RPCCanonicalJoin_RespectsBoundary(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		FilePath:    "user.proto",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser"},
	})
	reg.Add(Contract{
		ID:          "grpc::UserService::getUser",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		FilePath:    "api.ts",
		RepoPrefix:  "other-app",
		WorkspaceID: "globex", // different workspace — must NOT join
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "getUser"},
	})

	result := Match(reg)
	if len(result.Matched) != 0 {
		t.Fatalf("across-workspace contracts must not join: %+v", result.Matched)
	}
	if len(result.OrphanProviders) != 1 || len(result.OrphanConsumers) != 1 {
		t.Errorf("both sides stay orphaned: providers=%d consumers=%d",
			len(result.OrphanProviders), len(result.OrphanConsumers))
	}
}

// TestMatch_RPCCanonicalJoin_NoWrongMethodJoin: a method-level
// consumer with no matching provider method must not join a different
// method's provider.
func TestMatch_RPCCanonicalJoin_NoWrongMethodJoin(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Contract{
		ID:          "grpc::UserService::DeleteUser",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		FilePath:    "user.proto",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "DeleteUser"},
	})
	reg.Add(Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		FilePath:    "client.go",
		RepoPrefix:  "gateway",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser"},
	})

	result := Match(reg)
	if len(result.Matched) != 0 {
		t.Fatalf("different methods must not join: %+v", result.Matched)
	}
}

// TestMatch_RPCCanonicalJoin_PackageQualifiedService: a provider whose
// Meta carries a package-qualified service name joins a bare-named
// consumer of the same service.
func TestMatch_RPCCanonicalJoin_PackageQualifiedService(t *testing.T) {
	reg := NewRegistry()
	reg.Add(Contract{
		ID:          "grpc::billing.v1.UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleProvider,
		FilePath:    "user.proto",
		RepoPrefix:  "svc-users",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "billing.v1.UserService", "method": "GetUser"},
	})
	reg.Add(Contract{
		ID:          "grpc::UserService::GetUser",
		Type:        ContractGRPC,
		Role:        RoleConsumer,
		FilePath:    "client.go",
		RepoPrefix:  "gateway",
		WorkspaceID: "acme",
		ProjectID:   "users",
		Meta:        map[string]any{"service": "UserService", "method": "GetUser"},
	})

	result := Match(reg)
	if len(result.Matched) != 1 {
		t.Fatalf("package-qualified service should join bare-named consumer, got %d", len(result.Matched))
	}
}
