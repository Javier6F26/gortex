package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// Spring
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Java_Spring_RequestBodyAndResponseEntity(t *testing.T) {
	src := []byte(`package com.example.api;

import org.springframework.web.bind.annotation.*;
import org.springframework.http.ResponseEntity;

@RestController
@RequestMapping("/users")
public class UsersController {

    @PostMapping("/")
    @ResponseStatus(HttpStatus.CREATED)
    public ResponseEntity<UserResp> create(@RequestBody CreateUserReq body,
                                           @RequestParam("tenant") String tenant) {
        return ResponseEntity.ok(new UserResp());
    }
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/UsersController.java::UsersController", Name: "UsersController", Kind: graph.KindType, FilePath: "pkg/UsersController.java", StartLine: 6, EndLine: 15},
		{ID: "pkg/UsersController.java::UsersController.create", Name: "create", Kind: graph.KindMethod, FilePath: "pkg/UsersController.java", StartLine: 10, EndLine: 15},
		{ID: "pkg/UsersController.java::CreateUserReq", Name: "CreateUserReq", Kind: graph.KindType, FilePath: "pkg/UsersController.java", StartLine: 0, EndLine: 0},
		{ID: "pkg/UsersController.java::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/UsersController.java", StartLine: 0, EndLine: 0},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/UsersController.java", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/UsersController.java::CreateUserReq")
	assertMetaString(t, c, "response_type", "pkg/UsersController.java::UserResp")
	assertMetaStrings(t, c, "query_params", []string{"tenant"})
	assertMetaInts(t, c, "status_codes", []int{201})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// Kotlin (via Spring enricher)
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Kotlin_Spring_RequestAndReturn(t *testing.T) {
	src := []byte(`package com.example.api

import org.springframework.web.bind.annotation.*

@RestController
@RequestMapping("/users")
class UsersController {
    @PostMapping("/")
    fun create(@RequestBody body: CreateUserReq, @RequestParam("tenant") tenant: String): UserResp {
        return UserResp()
    }
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/UsersController.kt::UsersController", Name: "UsersController", Kind: graph.KindType, FilePath: "pkg/UsersController.kt", StartLine: 5, EndLine: 12},
		{ID: "pkg/UsersController.kt::UsersController.create", Name: "create", Kind: graph.KindMethod, FilePath: "pkg/UsersController.kt", StartLine: 8, EndLine: 11},
		{ID: "pkg/UsersController.kt::CreateUserReq", Name: "CreateUserReq", Kind: graph.KindType, FilePath: "pkg/UsersController.kt", StartLine: 0, EndLine: 0},
		{ID: "pkg/UsersController.kt::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/UsersController.kt", StartLine: 0, EndLine: 0},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/UsersController.kt", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/UsersController.kt::CreateUserReq")
	assertMetaString(t, c, "response_type", "pkg/UsersController.kt::UserResp")
	assertMetaStrings(t, c, "query_params", []string{"tenant"})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// JAX-RS
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Java_JAXRS_TypedParamAndReturn(t *testing.T) {
	src := []byte(`package com.example.api;

import javax.ws.rs.*;

@Path("/users")
public class UsersResource {

    @POST
    @Path("/")
    public UserResp create(@QueryParam("tenant") String tenant, CreateUserReq body) {
        return new UserResp();
    }
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/UsersResource.java::UsersResource", Name: "UsersResource", Kind: graph.KindType, FilePath: "pkg/UsersResource.java", StartLine: 5, EndLine: 12},
		{ID: "pkg/UsersResource.java::UsersResource.create", Name: "create", Kind: graph.KindMethod, FilePath: "pkg/UsersResource.java", StartLine: 8, EndLine: 12},
		{ID: "pkg/UsersResource.java::CreateUserReq", Name: "CreateUserReq", Kind: graph.KindType, FilePath: "pkg/UsersResource.java", StartLine: 0, EndLine: 0},
		{ID: "pkg/UsersResource.java::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/UsersResource.java", StartLine: 0, EndLine: 0},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/UsersResource.java", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/", RoleProvider)

	// JAX-RS's first-param heuristic: first typed param that isn't a
	// helper type. `@QueryParam("tenant") String tenant` is skipped
	// because String is a helper; `CreateUserReq body` is the body.
	assertMetaString(t, c, "request_type", "pkg/UsersResource.java::CreateUserReq")
	assertMetaString(t, c, "response_type", "pkg/UsersResource.java::UserResp")
	assertMetaStrings(t, c, "query_params", []string{"tenant"})
	assertMetaString(t, c, "schema_source", "extracted")
}
