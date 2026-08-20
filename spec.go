package composition

import (
	"reflect"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ParamIn says where an HTTP request parameter lives.
type ParamIn string

// Parameter locations, matching the OpenAPI "in" vocabulary.
const (
	InPath   ParamIn = "path"
	InQuery  ParamIn = "query"
	InHeader ParamIn = "header"
)

// ParamSpec describes one named HTTP request parameter consumed by a
// binder: where it lives, its schema type and whether it is required.
// Binders from the bind/ subpackage expose it via [ParamDocumenter];
// documentation generators (the openapi subpackage) consume it.
type ParamSpec struct {
	In   ParamIn
	Name string

	// Type is the OpenAPI schema type of the parameter value:
	// "string", "integer", "number" or "boolean".
	Type string
	// Format is the optional OpenAPI schema format ("int32", "int64",
	// "double", ...).
	Format string
	// Enum lists the allowed values for enum-typed parameters
	// (canonical proto value names).
	Enum []string

	Required bool
}

// BodySpec describes the request body consumed by a body binder. At most
// one of Proto / DTO is set for a typed body; both nil means the body is
// consumed but its schema is unknown (bind.Body with a custom parser).
type BodySpec struct {
	// Proto is a prototype of the protojson-decoded body message
	// (bind.BodyJSON).
	Proto proto.Message
	// DTO is the type of the JSON-decoded DTO body
	// (bind.BodyJSONInto / bind.BodyJSONMap).
	DTO reflect.Type
}

// ParamDocumenter is implemented by binders that consume a named HTTP
// parameter and can describe it for documentation generation. Binders
// without it (e.g. bind.Ctx, hand-written [BinderFunc]) simply do not
// appear in generated documents.
type ParamDocumenter interface {
	ParamSpec() ParamSpec
}

// BodyDocumenter is implemented by binders that consume the request body.
type BodyDocumenter interface {
	BodySpec() BodySpec
}

// Doc carries optional human-facing documentation for one route,
// installed via [Route.Doc]. All fields map one-to-one onto the OpenAPI
// operation object.
type Doc struct {
	OperationID string
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool
}

// OperationInfo is one App-registered proxy route as seen by
// documentation generators: the HTTP surface (verb, pattern, parameters,
// body) plus the response shape. Obtained via [App.Operations]; consumed
// by the openapi subpackage.
type OperationInfo struct {
	Method  string // HTTP verb
	Pattern string // ServeMux pattern without the verb

	Doc           Doc
	SuccessStatus int

	Params []ParamSpec
	Body   *BodySpec // nil when no binder consumes the body

	// ResponseProto is a prototype of the proto response message; nil
	// when a Map transformer is installed or the response is not a
	// proto message.
	ResponseProto proto.Message
	// ResponseDTO is the DTO type returned by the Map transformer, when
	// one is installed.
	ResponseDTO reflect.Type

	// Marshal is the effective protojson options of the route — field
	// naming in generated schemas follows it (UseProtoNames,
	// UseEnumNumbers).
	Marshal protojson.MarshalOptions
}
