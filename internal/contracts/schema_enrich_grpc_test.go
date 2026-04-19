package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestGRPCEnrich_Go_Consumer_InlineRequestLiteral(t *testing.T) {
	src := []byte(`package main

import (
	"context"
	pb "example.com/api/users"
)

func fetchUser(ctx context.Context, conn Conn) (*pb.GetUserResponse, error) {
	client := pb.NewUsersClient(conn)
	return client.GetUser(ctx, &pb.GetUserRequest{Id: "abc"})
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/main.go::fetchUser", Name: "fetchUser", Kind: graph.KindFunction, FilePath: "pkg/main.go", StartLine: 8, EndLine: 11},
	}
	cs := (&GRPCExtractor{}).Extract("pkg/main.go", src, nodes, nil)
	c := findContract(t, cs, "grpc::Users::GetUser", RoleConsumer)
	assertMetaString(t, c, "request_type", "pb.GetUserRequest")
	assertMetaString(t, c, "schema_source", "extracted")
}

func TestGRPCEnrich_Go_Consumer_VariableRequest(t *testing.T) {
	src := []byte(`package main

import (
	"context"
	pb "example.com/api/users"
)

func fetchUser(ctx context.Context, conn Conn) (*pb.GetUserResponse, error) {
	client := pb.NewUsersClient(conn)
	req := &pb.GetUserRequest{Id: "abc"}
	return client.GetUser(ctx, req)
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/main.go::fetchUser", Name: "fetchUser", Kind: graph.KindFunction, FilePath: "pkg/main.go", StartLine: 8, EndLine: 12},
	}
	cs := (&GRPCExtractor{}).Extract("pkg/main.go", src, nodes, nil)
	c := findContract(t, cs, "grpc::Users::GetUser", RoleConsumer)
	assertMetaString(t, c, "request_type", "pb.GetUserRequest")
	assertMetaString(t, c, "schema_source", "extracted")
}

func TestGRPCEnrich_Proto_RequestResponseTypes(t *testing.T) {
	src := []byte(`syntax = "proto3";
package users;

message GetUserRequest { string id = 1; }
message GetUserResponse { string name = 1; }
message ListUsersRequest {}
message ListUsersResponse { repeated GetUserResponse users = 1; }

service Users {
  rpc GetUser(GetUserRequest) returns (GetUserResponse);
  rpc ListUsers(ListUsersRequest) returns (stream GetUserResponse);
  rpc Chat(stream common.Message) returns (stream common.Reply);
}
`)
	cs := (&GRPCExtractor{}).Extract("proto/users.proto", src, nil, nil)

	c := findContract(t, cs, "grpc::Users::GetUser", RoleProvider)
	assertMetaString(t, c, "request_type", "GetUserRequest")
	assertMetaString(t, c, "response_type", "GetUserResponse")
	assertMetaString(t, c, "schema_source", "extracted")
	if _, streamed := c.Meta["request_stream"]; streamed {
		t.Errorf("GetUser should not have request_stream flag")
	}

	c = findContract(t, cs, "grpc::Users::ListUsers", RoleProvider)
	assertMetaString(t, c, "response_type", "GetUserResponse")
	if v, ok := c.Meta["response_stream"].(bool); !ok || !v {
		t.Errorf("ListUsers should have response_stream=true, got %v", c.Meta["response_stream"])
	}

	c = findContract(t, cs, "grpc::Users::Chat", RoleProvider)
	assertMetaString(t, c, "request_type", "common.Message")
	assertMetaString(t, c, "response_type", "common.Reply")
	if v, _ := c.Meta["request_stream"].(bool); !v {
		t.Errorf("Chat should have request_stream=true")
	}
	if v, _ := c.Meta["response_stream"].(bool); !v {
		t.Errorf("Chat should have response_stream=true")
	}
}
