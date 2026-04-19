package contracts

import "testing"

func TestOpenAPI_YAML_RequestResponseAndStatus(t *testing.T) {
	src := []byte(`openapi: 3.0.0
info:
  title: Users
  version: "1.0"
paths:
  /users:
    post:
      summary: Create user
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/CreateUserReq'
      responses:
        '201':
          description: Created
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/UserResp'
        '400':
          description: Bad request
components:
  schemas:
    CreateUserReq:
      type: object
    UserResp:
      type: object
`)
	cs := (&OpenAPIExtractor{}).Extract("api/users.yaml", src, nil, nil)
	c := findContract(t, cs, "http::POST::/users", RoleProvider)

	assertMetaString(t, c, "request_type", "CreateUserReq")
	assertMetaString(t, c, "response_type", "UserResp")
	assertMetaInts(t, c, "status_codes", []int{201, 400})
	assertMetaString(t, c, "schema_source", "extracted")
}

func TestOpenAPI_YAML_NoBodies_SchemaSourceNone(t *testing.T) {
	src := []byte(`openapi: 3.0.0
paths:
  /health:
    get:
      responses:
        '200':
          description: OK
`)
	cs := (&OpenAPIExtractor{}).Extract("api/health.yaml", src, nil, nil)
	c := findContract(t, cs, "http::GET::/health", RoleProvider)
	assertMetaString(t, c, "schema_source", "none")
	assertMetaInts(t, c, "status_codes", []int{200})
}

func TestOpenAPI_JSON_RequestResponse(t *testing.T) {
	src := []byte(`{
  "openapi": "3.0.0",
  "paths": {
    "/users": {
      "post": {
        "requestBody": { "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CreateUserReq" } } } },
        "responses": { "201": { "content": { "application/json": { "schema": { "$ref": "#/components/schemas/UserResp" } } } } }
      }
    }
  }
}`)
	cs := (&OpenAPIExtractor{}).Extract("api/users.json", src, nil, nil)
	c := findContract(t, cs, "http::POST::/users", RoleProvider)
	assertMetaString(t, c, "request_type", "CreateUserReq")
	assertMetaString(t, c, "response_type", "UserResp")
}
