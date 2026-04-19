package contracts

import (
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestHTTPEnrich_Dart_Dio_DataAndFromJson(t *testing.T) {
	src := []byte(`import 'package:dio/dio.dart';

class CreateTuckReq { final String title; CreateTuckReq(this.title); }
class CreateTuckResp { static CreateTuckResp fromJson(dynamic j) => CreateTuckResp(); }

final dio = Dio();

Future<CreateTuckResp> createTuck() async {
  final payload = CreateTuckReq('a');
  final resp = await dio.post('/v1/tucks', data: payload);
  return CreateTuckResp.fromJson(resp.data);
}
`)
	nodes := []*graph.Node{
		{ID: "lib/api.dart::createTuck", Name: "createTuck", Kind: graph.KindFunction, FilePath: "lib/api.dart", StartLine: 8, EndLine: 12},
		{ID: "lib/api.dart::CreateTuckReq", Name: "CreateTuckReq", Kind: graph.KindType, FilePath: "lib/api.dart", StartLine: 3, EndLine: 3},
		{ID: "lib/api.dart::CreateTuckResp", Name: "CreateTuckResp", Kind: graph.KindType, FilePath: "lib/api.dart", StartLine: 4, EndLine: 4},
	}
	cs := (&HTTPExtractor{}).Extract("lib/api.dart", src, nodes, nil)
	c := findContract(t, cs, "http::POST::/v1/tucks", RoleConsumer)

	assertMetaString(t, c, "request_type", "lib/api.dart::CreateTuckReq")
	assertMetaString(t, c, "response_type", "lib/api.dart::CreateTuckResp")
	assertMetaString(t, c, "schema_source", "extracted")
}

func TestHTTPEnrich_Dart_PackageHttp_BodyJSONEncode(t *testing.T) {
	src := []byte(`import 'dart:convert';
import 'package:http/http.dart' as http;

class UpdateReq { final String v; UpdateReq(this.v); }
class UpdateResp { static UpdateResp fromJson(dynamic j) => UpdateResp(); }

Future<UpdateResp> update() async {
  final payload = UpdateReq('x');
  final resp = await http.put(Uri.parse('/v1/item'), body: jsonEncode(payload));
  return UpdateResp.fromJson(resp.body);
}
`)
	nodes := []*graph.Node{
		{ID: "lib/client.dart::update", Name: "update", Kind: graph.KindFunction, FilePath: "lib/client.dart", StartLine: 7, EndLine: 11},
		{ID: "lib/client.dart::UpdateReq", Name: "UpdateReq", Kind: graph.KindType, FilePath: "lib/client.dart", StartLine: 4, EndLine: 4},
		{ID: "lib/client.dart::UpdateResp", Name: "UpdateResp", Kind: graph.KindType, FilePath: "lib/client.dart", StartLine: 5, EndLine: 5},
	}
	cs := (&HTTPExtractor{}).Extract("lib/client.dart", src, nodes, nil)
	c := findContract(t, cs, "http::PUT::/v1/item", RoleConsumer)

	assertMetaString(t, c, "request_type", "lib/client.dart::UpdateReq")
	assertMetaString(t, c, "response_type", "lib/client.dart::UpdateResp")
	assertMetaString(t, c, "schema_source", "extracted")
}
