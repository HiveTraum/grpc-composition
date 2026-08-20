package openapi_test

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/apipb"
	"google.golang.org/protobuf/types/known/typepb"

	"github.com/HiveTraum/grpc-composition"
	"github.com/HiveTraum/grpc-composition/bind"
	"github.com/HiveTraum/grpc-composition/openapi"
)

// apiClient is a mock unary client over apipb.Api — a real proto3 message
// with scalar, enum, repeated-message and message fields, so schema
// generation is exercised without local codegen.
type apiClient struct{}

func (apiClient) Get(_ context.Context, req *apipb.Api, _ ...grpc.CallOption) (*apipb.Api, error) {
	return req, nil
}

func newDoc(t *testing.T, build func(app *composition.App)) *openapi.Document {
	t.Helper()
	app := composition.New()
	build(app)
	return openapi.Generate(app, openapi.Info{Title: "test", Version: "0.0.1"})
}

func TestGenerate_ProxyRoute(t *testing.T) {
	var c apiClient
	doc := newDoc(t, func(app *composition.App) {
		app.Get("/apis/{name}", c.Get,
			bind.PathString("name", func(req *apipb.Api, v string) { req.Name = v }),
			bind.QueryEnum("syntax", func(req *apipb.Api, v typepb.Syntax) { req.Syntax = v }),
		).Doc(composition.Doc{OperationID: "get-api", Summary: "Get API", Tags: []string{"apis"}})
	})

	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version: %q", doc.OpenAPI)
	}
	item := doc.Paths["/apis/{name}"]
	if item == nil || item.Get == nil {
		t.Fatalf("missing GET /apis/{name}: %+v", doc.Paths)
	}
	op := item.Get
	if op.OperationID != "get-api" || op.Summary != "Get API" || len(op.Tags) != 1 {
		t.Fatalf("doc fields: %+v", op)
	}

	if len(op.Parameters) != 2 {
		t.Fatalf("parameters: %+v", op.Parameters)
	}
	name := op.Parameters[0]
	if name.Name != "name" || name.In != "path" || !name.Required || name.Schema.Type != "string" {
		t.Fatalf("path param: %+v", name)
	}
	syntax := op.Parameters[1]
	if syntax.Name != "syntax" || syntax.In != "query" || syntax.Required {
		t.Fatalf("query param: %+v", syntax)
	}
	if !slices.Contains(syntax.Schema.Enum, "SYNTAX_PROTO3") {
		t.Fatalf("enum values: %v", syntax.Schema.Enum)
	}

	resp := op.Responses["200"]
	if resp == nil {
		t.Fatalf("responses: %+v", op.Responses)
	}
	if ref := resp.Content["application/json"].Schema.Ref; ref != "#/components/schemas/google.protobuf.Api" {
		t.Fatalf("response schema ref: %q", ref)
	}

	api := doc.Components.Schemas["google.protobuf.Api"]
	if api == nil {
		t.Fatal("missing Api component")
	}
	if api.Properties["sourceContext"] == nil {
		t.Fatalf("expected camelCase sourceContext property, got %v", api.Properties)
	}
	methods := api.Properties["methods"]
	if methods.Type != "array" || methods.Items.Ref != "#/components/schemas/google.protobuf.Method" {
		t.Fatalf("methods property: %+v", methods)
	}
	if doc.Components.Schemas["google.protobuf.Method"] == nil {
		t.Fatal("missing Method component (recursive message schema)")
	}

	def := op.Responses["default"]
	if def == nil {
		t.Fatal("missing default error response")
	}
	if ref := def.Content["application/problem+json"].Schema.Ref; ref != "#/components/schemas/composition.ProblemDetails" {
		t.Fatalf("default response schema ref: %q", ref)
	}
	prob := doc.Components.Schemas["composition.ProblemDetails"]
	if prob == nil || prob.Properties["reason"] == nil || prob.Properties["detail"] == nil {
		t.Fatalf("ProblemDetails component: %+v", prob)
	}
}

func TestGenerate_RequestBody(t *testing.T) {
	var c apiClient
	doc := newDoc(t, func(app *composition.App) {
		app.Post("/apis", c.Get, bind.BodyJSON[apipb.Api]()).OnSuccess(201)
	})

	op := doc.Paths["/apis"].Post
	if op == nil {
		t.Fatal("missing POST /apis")
	}
	rb := op.RequestBody
	if rb == nil || !rb.Required {
		t.Fatalf("request body: %+v", rb)
	}
	if ref := rb.Content["application/json"].Schema.Ref; ref != "#/components/schemas/google.protobuf.Api" {
		t.Fatalf("body schema ref: %q", ref)
	}
	if op.Responses["201"] == nil {
		t.Fatalf("responses: %+v", op.Responses)
	}
}

type userDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Hidden      string `json:"-"`
	Plain       int
}

func TestGenerate_MapDTO(t *testing.T) {
	var c apiClient
	doc := newDoc(t, func(app *composition.App) {
		app.Get("/dto", c.Get).Map(func(a *apipb.Api) userDTO { return userDTO{ID: a.Name} })
	})

	schema := doc.Paths["/dto"].Get.Responses["200"].Content["application/json"].Schema
	if schema.Ref != "#/components/schemas/openapi_test.userDTO" {
		t.Fatalf("response schema ref: %q", schema.Ref)
	}
	dto := doc.Components.Schemas["openapi_test.userDTO"]
	if dto == nil {
		t.Fatal("missing DTO component")
	}
	if dto.Properties["id"] == nil || dto.Properties["display_name"] == nil {
		t.Fatalf("json tag names: %v", dto.Properties)
	}
	if dto.Properties["Hidden"] != nil || dto.Properties["-"] != nil {
		t.Fatalf("json:\"-\" field must be skipped: %v", dto.Properties)
	}
	if dto.Properties["Plain"] == nil || dto.Properties["Plain"].Type != "integer" {
		t.Fatalf("untagged field: %v", dto.Properties)
	}
}

func TestGenerate_UseProtoNames(t *testing.T) {
	var c apiClient
	doc := newDoc(t, func(app *composition.App) {
		app.Get("/apis", c.Get).
			WithMarshalOptions(protojson.MarshalOptions{UseProtoNames: true})
	})

	api := doc.Components.Schemas["google.protobuf.Api"]
	if api.Properties["source_context"] == nil {
		t.Fatalf("expected snake_case source_context with UseProtoNames, got %v", api.Properties)
	}
}
