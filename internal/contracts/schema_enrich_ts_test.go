package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

// -----------------------------------------------------------------------------
// NestJS
// -----------------------------------------------------------------------------

func TestHTTPEnrich_TS_NestJS_BodyQueryReturn(t *testing.T) {
	src := []byte(`import { Controller, Post, Body, Query, HttpCode, HttpStatus } from '@nestjs/common'
import { CreateUserDto } from './dto'
import { UserResp } from './resp'

@Controller('users')
export class UsersController {
  @Post('/')
  @HttpCode(201)
  create(@Body() dto: CreateUserDto, @Query('tenant') tenant: string): Promise<UserResp> {
    return Promise.resolve({} as UserResp)
  }
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/users.ts::UsersController", Name: "UsersController", Kind: graph.KindType, FilePath: "pkg/users.ts", StartLine: 5, EndLine: 11},
		{ID: "pkg/users.ts::UsersController.create", Name: "create", Kind: graph.KindMethod, FilePath: "pkg/users.ts", StartLine: 9, EndLine: 11},
		{ID: "pkg/users.ts::CreateUserDto", Name: "CreateUserDto", Kind: graph.KindType, FilePath: "pkg/users.ts", StartLine: 2, EndLine: 2},
		{ID: "pkg/users.ts::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/users.ts", StartLine: 3, EndLine: 3},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/users.ts", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/users.ts::CreateUserDto")
	assertMetaString(t, c, "response_type", "pkg/users.ts::UserResp")
	assertMetaStrings(t, c, "query_params", []string{"tenant"})
	assertMetaInts(t, c, "status_codes", []int{201})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// Express
// -----------------------------------------------------------------------------

func TestHTTPEnrich_TS_Express_BodyCastResJSON(t *testing.T) {
	src := []byte(`import type { Request, Response } from 'express'
import { UserReq } from './req'
import { UserResp } from './resp'

export function register(app: any) {
  app.post('/users', createUser)
}

function createUser(req: Request, res: Response) {
  const body = req.body as UserReq
  const result: UserResp = toResp(body)
  res.status(201).json(result)
}

function toResp(_: UserReq): UserResp { return {} as UserResp }
`)
	nodes := []*graph.Node{
		{ID: "pkg/api.ts::register", Name: "register", Kind: graph.KindFunction, FilePath: "pkg/api.ts", StartLine: 5, EndLine: 7},
		{ID: "pkg/api.ts::createUser", Name: "createUser", Kind: graph.KindFunction, FilePath: "pkg/api.ts", StartLine: 9, EndLine: 13},
		{ID: "pkg/api.ts::UserReq", Name: "UserReq", Kind: graph.KindType, FilePath: "pkg/api.ts", StartLine: 2, EndLine: 2},
		{ID: "pkg/api.ts::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/api.ts", StartLine: 3, EndLine: 3},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/api.ts", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/users", RoleProvider)

	assertMetaString(t, c, "request_type", "pkg/api.ts::UserReq")
	assertMetaString(t, c, "response_type", "pkg/api.ts::UserResp")
	assertMetaInts(t, c, "status_codes", []int{201})
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// Axios consumer
// -----------------------------------------------------------------------------

func TestHTTPEnrich_TS_Axios_GenericsAndPayload(t *testing.T) {
	src := []byte(`import axios from 'axios'
import type { UserReq, UserResp } from './types'

export async function createUser(payload: UserReq): Promise<UserResp> {
  const { data } = await axios.post<UserResp, UserReq>('/api/users', payload)
  return data
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/client.ts::createUser", Name: "createUser", Kind: graph.KindFunction, FilePath: "pkg/client.ts", StartLine: 4, EndLine: 7},
		{ID: "pkg/client.ts::UserReq", Name: "UserReq", Kind: graph.KindType, FilePath: "pkg/client.ts", StartLine: 2, EndLine: 2},
		{ID: "pkg/client.ts::UserResp", Name: "UserResp", Kind: graph.KindType, FilePath: "pkg/client.ts", StartLine: 2, EndLine: 2},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/client.ts", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/api/users", RoleConsumer)

	assertMetaString(t, c, "request_type", "pkg/client.ts::UserReq")
	assertMetaString(t, c, "response_type", "pkg/client.ts::UserResp")
	assertMetaString(t, c, "schema_source", "extracted")
}

// -----------------------------------------------------------------------------
// Fetch consumer
// -----------------------------------------------------------------------------

func TestHTTPEnrich_TS_Fetch_StringifyAndCast(t *testing.T) {
	src := []byte(`import type { TuckReq, TuckResp } from './types'

export async function createTuck(): Promise<TuckResp> {
  const payload: TuckReq = { title: 'a' }
  const resp = await fetch('/v1/tucks', {
    method: 'POST',
    body: JSON.stringify(payload),
  })
  const data = (await resp.json()) as TuckResp
  return data
}
`)
	nodes := []*graph.Node{
		{ID: "pkg/client.ts::createTuck", Name: "createTuck", Kind: graph.KindFunction, FilePath: "pkg/client.ts", StartLine: 3, EndLine: 10},
		{ID: "pkg/client.ts::TuckReq", Name: "TuckReq", Kind: graph.KindType, FilePath: "pkg/client.ts", StartLine: 1, EndLine: 1},
		{ID: "pkg/client.ts::TuckResp", Name: "TuckResp", Kind: graph.KindType, FilePath: "pkg/client.ts", StartLine: 1, EndLine: 1},
	}
	cs := (&HTTPExtractor{}).Extract("pkg/client.ts", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/v1/tucks", RoleConsumer)

	assertMetaString(t, c, "request_type", "pkg/client.ts::TuckReq")
	assertMetaString(t, c, "response_type", "pkg/client.ts::TuckResp")
	assertMetaString(t, c, "schema_source", "extracted")
}
