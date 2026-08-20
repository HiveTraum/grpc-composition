// Package openapi generates an OpenAPI 3.1 document from the binder
// metadata of an [composition.App]: registered proxy routes become path
// operations, binder specs become parameters and request bodies, and
// proto descriptors / Map DTO types become response schemas following
// protojson / encoding/json serialization rules.
//
//	doc := openapi.Generate(app, openapi.Info{Title: "users", Version: "1.0.0"})
//	data, _ := json.Marshal(doc)
//
// The document is plain data — adopters may post-process it (merge with
// an existing contract, add servers or security schemes) before
// serialization. Only routes registered via the App verb methods are
// documented: [composition.Aggregate] endpoints and handlers on foreign
// routers are opaque to the generator.
package openapi

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/HiveTraum/grpc-composition"
)

// Info is the OpenAPI info object.
type Info struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// Document is an OpenAPI 3.1 document.
type Document struct {
	OpenAPI    string               `json:"openapi"`
	Info       Info                 `json:"info"`
	Paths      map[string]*PathItem `json:"paths,omitempty"`
	Components *Components          `json:"components,omitempty"`
}

// PathItem holds the operations of one URL path.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
}

// Operation is one HTTP operation.
type Operation struct {
	OperationID string               `json:"operationId,omitempty"`
	Summary     string               `json:"summary,omitempty"`
	Description string               `json:"description,omitempty"`
	Tags        []string             `json:"tags,omitempty"`
	Deprecated  bool                 `json:"deprecated,omitempty"`
	Parameters  []*Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody         `json:"requestBody,omitempty"`
	Responses   map[string]*Response `json:"responses,omitempty"`
}

// Parameter is one path / query / header parameter.
type Parameter struct {
	Name     string  `json:"name"`
	In       string  `json:"in"`
	Required bool    `json:"required,omitempty"`
	Schema   *Schema `json:"schema,omitempty"`
}

// RequestBody describes the request body of an operation.
type RequestBody struct {
	Required bool                  `json:"required,omitempty"`
	Content  map[string]*MediaType `json:"content"`
}

// MediaType couples a content type with its schema.
type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Response is one documented response of an operation.
type Response struct {
	Description string                `json:"description"`
	Content     map[string]*MediaType `json:"content,omitempty"`
}

// Components holds the reusable schemas referenced from operations.
type Components struct {
	Schemas map[string]*Schema `json:"schemas,omitempty"`
}

// Schema is a JSON Schema subset sufficient for protojson / encoding/json
// shapes. An empty Schema means "any value".
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
}

// Source is the route inventory the generator consumes;
// *[composition.App] implements it.
type Source interface {
	Operations() []composition.OperationInfo
}

// Generate builds the OpenAPI document for every proxy route registered
// on src. Call it after all routes are registered.
func Generate(src Source, info Info) *Document {
	g := &generator{schemas: make(map[string]*Schema)}
	doc := &Document{
		OpenAPI: "3.1.0",
		Info:    info,
		Paths:   make(map[string]*PathItem),
	}
	for _, op := range src.Operations() {
		path := normalizePath(op.Pattern)
		item := doc.Paths[path]
		if item == nil {
			item = &PathItem{}
			doc.Paths[path] = item
		}
		assign(item, op.Method, g.operation(op))
	}
	if len(g.schemas) > 0 {
		doc.Components = &Components{Schemas: g.schemas}
	}
	return doc
}

// normalizePath turns a ServeMux pattern into an OpenAPI path: the
// wildcard suffix `{name...}` becomes a plain `{name}` parameter and the
// exact-match marker `{$}` is dropped.
func normalizePath(pattern string) string {
	segs := strings.Split(pattern, "/")
	out := segs[:0]
	for _, seg := range segs {
		if seg == "{$}" {
			continue
		}
		if len(seg) >= 2 && seg[0] == '{' && strings.HasSuffix(seg, "...}") {
			seg = "{" + strings.TrimSuffix(seg[1:len(seg)-1], "...") + "}"
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

func assign(item *PathItem, method string, op *Operation) {
	switch method {
	case http.MethodGet:
		item.Get = op
	case http.MethodPost:
		item.Post = op
	case http.MethodPut:
		item.Put = op
	case http.MethodPatch:
		item.Patch = op
	case http.MethodDelete:
		item.Delete = op
	}
}

type generator struct {
	schemas map[string]*Schema
}

func (g *generator) operation(op composition.OperationInfo) *Operation {
	o := &Operation{
		OperationID: op.Doc.OperationID,
		Summary:     op.Doc.Summary,
		Description: op.Doc.Description,
		Tags:        op.Doc.Tags,
		Deprecated:  op.Doc.Deprecated,
	}

	for _, p := range op.Params {
		o.Parameters = append(o.Parameters, &Parameter{
			Name:     p.Name,
			In:       string(p.In),
			Required: p.Required,
			Schema:   &Schema{Type: p.Type, Format: p.Format, Enum: p.Enum},
		})
	}

	if op.Body != nil {
		o.RequestBody = &RequestBody{
			Required: true,
			Content:  map[string]*MediaType{"application/json": {Schema: g.bodySchema(op)}},
		}
	}

	o.Responses = map[string]*Response{
		strconv.Itoa(op.SuccessStatus): {
			Description: http.StatusText(op.SuccessStatus),
			Content:     map[string]*MediaType{"application/json": {Schema: g.responseSchema(op)}},
		},
		// Every error path goes through the route's error mapper; the
		// default mapper produces RFC 7807 problem details.
		"default": {
			Description: "Error",
			Content: map[string]*MediaType{
				"application/problem+json": {Schema: g.dtoSchema(reflect.TypeFor[composition.ProblemDetails]())},
			},
		},
	}
	return o
}

// bodySchema derives the request-body schema from the binder's BodySpec:
// proto prototype, DTO type, or unknown (empty schema).
func (g *generator) bodySchema(op composition.OperationInfo) *Schema {
	switch {
	case op.Body.Proto != nil:
		return g.protoSchema(op.Body.Proto, op.Marshal)
	case op.Body.DTO != nil:
		return g.dtoSchema(op.Body.DTO)
	default:
		return &Schema{}
	}
}

// responseSchema derives the success-response schema: the Map DTO type
// when a transformer is installed, otherwise the proto response message.
func (g *generator) responseSchema(op composition.OperationInfo) *Schema {
	switch {
	case op.ResponseDTO != nil:
		return g.dtoSchema(op.ResponseDTO)
	case op.ResponseProto != nil:
		return g.protoSchema(op.ResponseProto, op.Marshal)
	default:
		return &Schema{}
	}
}
