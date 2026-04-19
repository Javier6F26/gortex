package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// FastAPI
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Py_FastAPI_RequestResponseStatus(t *testing.T) {
	src := []byte(`from fastapi import APIRouter, Query, status
from .models import CreateUserReq, UserResp

router = APIRouter()

@router.post('/users', status_code=status.HTTP_201_CREATED, response_model=UserResp)
def create_user(payload: CreateUserReq, tenant: str = Query(...)) -> UserResp:
    return UserResp()
`)
	nodes := []*graph.Node{
		{ID: "pkg/routes.py::create_user", Name: "create_user", Kind: graph.KindFunction, FilePath: "pkg/routes.py", StartLine: 6, EndLine: 8},
		{ID: "pkg/routes.py::CreateUserReq", Name: "CreateUserReq", Kind: graph.KindType, FilePath: "pkg/routes.py", StartLine: 2, EndLine: 2},
		{ID: "pkg/routes.py::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/routes.py", StartLine: 2, EndLine: 2},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/routes.py", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/users", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/routes.py::CreateUserReq")
	assertMetaString(t, c, "response_type", "pkg/routes.py::UserResp")
	assertMetaStrings(t, c, "query_params", []string{"tenant"})
	assertMetaInts(t, c, "status_codes", []int{201})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// Flask
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Py_Flask_RequestJSON_Jsonify(t *testing.T) {
	src := []byte(`from flask import Flask, request, jsonify
from .models import CreateReq

app = Flask(__name__)

@app.post('/users')
def create_user():
    payload: CreateReq = request.get_json()
    limit = request.args.get('limit')
    _ = limit
    result: CreateReq = payload
    return jsonify(result), 201
`)
	nodes := []*graph.Node{
		{ID: "pkg/app.py::create_user", Name: "create_user", Kind: graph.KindFunction, FilePath: "pkg/app.py", StartLine: 6, EndLine: 12},
		{ID: "pkg/app.py::CreateReq", Name: "CreateReq", Kind: graph.KindType, FilePath: "pkg/app.py", StartLine: 2, EndLine: 2},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/app.py", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/users", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/app.py::CreateReq")
	assertMetaString(t, c, "response_type", "pkg/app.py::CreateReq")
	assertMetaStrings(t, c, "query_params", []string{"limit"})
	assertMetaInts(t, c, "status_codes", []int{201})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// requests / httpx consumer
// -----------------------------------------------------------------------------

func TestHTTPEnrich_Py_Requests_JSONPayloadAndDecode(t *testing.T) {
	src := []byte(`import requests
from typing import cast
from .models import TuckReq, TuckResp

def create_tuck(payload: TuckReq) -> TuckResp:
    r = requests.post('/v1/tucks', json=payload)
    data: TuckResp = r.json()
    return data
`)
	nodes := []*graph.Node{
		{ID: "pkg/client.py::create_tuck", Name: "create_tuck", Kind: graph.KindFunction, FilePath: "pkg/client.py", StartLine: 5, EndLine: 8},
		{ID: "pkg/client.py::TuckReq", Name: "TuckReq", Kind: graph.KindType, FilePath: "pkg/client.py", StartLine: 3, EndLine: 3},
		{ID: "pkg/client.py::TuckResp", Name: "TuckResp", Kind: graph.KindType, FilePath: "pkg/client.py", StartLine: 3, EndLine: 3},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/client.py", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/v1/tucks", RoleConsumer)

	assertMetaString(t, c, "request_type", "pkg/client.py::TuckReq")
	assertMetaString(t, c, "response_type", "pkg/client.py::TuckResp")
	assertMetaString(t, c, "schema_source", "extracted")
}
