// Package composition provides a thin HTTP-to-gRPC proxy layer with
// type-safe request binding via Go generics.
//
// The primary entry point is [Proxy], which constructs an [http.Handler]
// that binds an incoming request into a protobuf message, invokes a unary
// gRPC method, and serializes the response.
//
// See README.md for product vision, scope and roadmap.
package composition

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Binder populates a typed request value from an [*http.Request].
//
// Binder constructors live in the bind/ subpackage; a hand-written binder
// wraps a plain function in [BinderFunc]. Binders that additionally
// implement [ParamDocumenter] or [BodyDocumenter] (all bind/ constructors
// do) contribute their metadata to generated OpenAPI documents — see
// [App.Operations] and the openapi subpackage.
type Binder[Req any] interface {
	Bind(r *http.Request, req *Req) error
}

// BinderFunc adapts a plain function to the [Binder] interface, mirroring
// [http.HandlerFunc]. A BinderFunc carries no metadata, so it never
// appears in generated OpenAPI documents.
type BinderFunc[Req any] func(r *http.Request, req *Req) error

// Bind implements [Binder].
func (f BinderFunc[Req]) Bind(r *http.Request, req *Req) error {
	return f(r, req)
}

// UnaryMethod is the canonical signature of any gRPC-generated unary client
// method. [Proxy] accepts method values of this shape and infers Req/Resp
// from them.
type UnaryMethod[Req, Resp any] func(ctx context.Context, req *Req, opts ...grpc.CallOption) (*Resp, error)

// PathExtractor reads a single named URL path parameter from a request.
//
// The framework calls the [PathExtractor] configured via
// [SetDefaultPathExtractor]; the default uses [http.Request.PathValue]
// (Go 1.22+). Routers that store path parameters elsewhere (e.g. chi) can
// be supported by registering their own extractor at program start:
//
//	composition.SetDefaultPathExtractor(chi.URLParam)
type PathExtractor func(r *http.Request, name string) string

var stdlibPathExtractor PathExtractor = func(r *http.Request, name string) string {
	return r.PathValue(name)
}

var defaultPathExtractor = stdlibPathExtractor

// SetDefaultPathExtractor replaces the [PathExtractor] used by bind.Path.
// Pass nil to restore the standard-library default.
//
// Intended to be called once at program startup; not safe for concurrent
// writes during request handling.
func SetDefaultPathExtractor(e PathExtractor) {
	if e == nil {
		defaultPathExtractor = stdlibPathExtractor
		return
	}
	defaultPathExtractor = e
}

// PathParam returns the named URL path parameter via the configured
// [PathExtractor]. Binder constructors in bind/ use this; user code
// normally does not call it directly.
func PathParam(r *http.Request, name string) string {
	return defaultPathExtractor(r, name)
}

var defaultMarshalOptions protojson.MarshalOptions

// SetDefaultMarshalOptions replaces the [protojson.MarshalOptions] used to
// serialize [proto.Message] responses. The zero value (the default) matches
// plain protojson.Marshal, where proto3 zero values are omitted from the
// output — REST APIs that promise stable field presence usually want
// EmitUnpopulated:
//
//	composition.SetDefaultMarshalOptions(protojson.MarshalOptions{
//	    EmitUnpopulated: true,
//	})
//
// The options apply to [Proxy] responses without a Map transformer and to
// [Aggregate] results that implement [proto.Message]; values serialized via
// encoding/json (Map DTOs, non-proto aggregates) are unaffected. Per-route
// override: [Route.WithMarshalOptions] / [AggregateRoute.WithMarshalOptions].
//
// Intended to be called once at program startup; not safe for concurrent
// writes during request handling.
func SetDefaultMarshalOptions(o protojson.MarshalOptions) {
	defaultMarshalOptions = o
}

// Route is an [http.Handler] that proxies an HTTP request to a unary gRPC method.
type Route[Req, Resp any] struct {
	method        UnaryMethod[Req, Resp]
	binders       []Binder[Req]
	mapper        func(*Resp) any
	mapOut        reflect.Type // DTO type of the Map transformer, for documentation
	successStatus int
	errorMapper   ErrorMapper               // nil → use package-level DefaultErrorMapper
	marshal       *protojson.MarshalOptions // nil → package-level default
	doc           Doc
}

// Proxy returns a [Route] that:
//
//  1. constructs a *Req via the given binders (applied in order)
//  2. invokes the gRPC method with r.Context()
//  3. serializes the response via protojson on success,
//     or maps a gRPC error via [DefaultErrorMapper] on failure
//
// Req and Resp are inferred from the method value; no explicit type
// parameters are required at the call site.
func Proxy[Req, Resp any](
	method UnaryMethod[Req, Resp],
	binders ...Binder[Req],
) *Route[Req, Resp] {
	return &Route[Req, Resp]{
		method:        method,
		binders:       binders,
		successStatus: http.StatusOK,
	}
}

// Map installs a response transformer applied between the gRPC call and
// serialization. Use it when the REST API should expose a different shape
// than the protobuf response (intentional differentiation).
//
// Out is the DTO type returned by fn; it is inferred from the callback, so
// the transformer keeps its natural signature instead of being widened to
// any:
//
//	composition.Proxy(users.GetUser, ...).
//	    Map(func(u *pb.User) UserDTO {
//	        return UserDTO{ID: u.Id, DisplayName: u.Name}
//	    })
//
// The returned value is serialized via encoding/json — protojson semantics
// no longer apply once the proto type is left behind.
func (rt *Route[Req, Resp]) Map[Out any](fn func(*Resp) Out) *Route[Req, Resp] {
	if fn == nil {
		rt.mapper = nil
		rt.mapOut = nil
		return rt
	}
	rt.mapper = func(resp *Resp) any { return fn(resp) }
	rt.mapOut = reflect.TypeFor[Out]()
	return rt
}

// OnSuccess overrides the HTTP status code written on a successful gRPC
// call. The default is 200 OK. Useful for POST endpoints that should
// return 201 Created.
func (rt *Route[Req, Resp]) OnSuccess(status int) *Route[Req, Resp] {
	rt.successStatus = status
	return rt
}

// WithErrorMapper overrides the package-level [DefaultErrorMapper] for
// this route only. Useful when one endpoint has special-case error
// formatting (e.g. a legacy API contract) while the rest of the service
// follows the default RFC 7807 shape.
//
// Body is the error-body type returned by fn, inferred from the callback,
// so a mapper with a concrete body type needs no any in its signature:
//
//	route.WithErrorMapper(func(err error) (int, LegacyError) {
//	    return 500, LegacyError{Code: "X-LEGACY"}
//	})
//
// An [ErrorMapper] value (Body = any) is accepted as well.
func (rt *Route[Req, Resp]) WithErrorMapper[Body any](fn func(error) (int, Body)) *Route[Req, Resp] {
	rt.errorMapper = toErrorMapper(fn)
	return rt
}

// WithMarshalOptions overrides the package-level protojson marshal options
// (see [SetDefaultMarshalOptions]) for this route only.
func (rt *Route[Req, Resp]) WithMarshalOptions(o protojson.MarshalOptions) *Route[Req, Resp] {
	rt.marshal = &o
	return rt
}

// Doc attaches human-facing documentation to the route — operation id,
// summary, tags — surfaced in generated OpenAPI documents. Purely
// declarative: runtime behavior does not change.
//
//	app.Get("/services", svc.ListServices, binders...).
//	    Doc(composition.Doc{OperationID: "list-services", Tags: []string{"services"}})
func (rt *Route[Req, Resp]) Doc(d Doc) *Route[Req, Resp] {
	rt.doc = d
	return rt
}

// describable is implemented by *Route to expose documentation metadata
// to [App.Operations] without the App knowing the route's type parameters.
type describable interface {
	operationInfo() OperationInfo
}

func (rt *Route[Req, Resp]) operationInfo() OperationInfo {
	info := OperationInfo{
		Doc:           rt.doc,
		SuccessStatus: rt.successStatus,
		Marshal:       rt.marshalOptions(),
	}
	for _, b := range rt.binders {
		if pd, ok := b.(ParamDocumenter); ok {
			info.Params = append(info.Params, pd.ParamSpec())
		}
		if bd, ok := b.(BodyDocumenter); ok {
			spec := bd.BodySpec()
			info.Body = &spec
		}
	}
	switch {
	case rt.mapOut != nil:
		info.ResponseDTO = rt.mapOut
	default:
		if pm, ok := any(new(Resp)).(proto.Message); ok {
			info.ResponseProto = pm
		}
	}
	return info
}

func (rt *Route[Req, Resp]) marshalOptions() protojson.MarshalOptions {
	if rt.marshal != nil {
		return *rt.marshal
	}
	return defaultMarshalOptions
}

// ServeHTTP implements [http.Handler].
func (rt *Route[Req, Resp]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req Req
	for _, b := range rt.binders {
		if err := b.Bind(r, &req); err != nil {
			code := bindErrorStatus(err)
			writeError(w, code, ProblemDetails{
				Status: code,
				Title:  http.StatusText(code),
				Detail: "bind: " + err.Error(),
			})
			return
		}
	}

	resp, err := rt.method(r.Context(), &req)
	if err != nil {
		mapper := rt.errorMapper
		if mapper == nil {
			mapper = DefaultErrorMapper
		}
		status, body := mapper(err)
		writeError(w, status, body)
		return
	}

	if rt.mapper != nil {
		writeJSON(w, rt.successStatus, rt.mapper(resp))
		return
	}
	writeResponse(w, rt.successStatus, resp, rt.marshalOptions())
}

// bindErrorStatus picks the HTTP status for a binder failure: 400 for
// malformed input, except an oversized request body ([http.MaxBytesError],
// produced by the bind.Body* size limit or an upstream
// [http.MaxBytesReader]) which is 413.
func bindErrorStatus(err error) int {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// writeError writes an error response. If the body is a [ProblemDetails],
// Content-Type is set to application/problem+json (RFC 7807); otherwise
// to application/json so custom mappers can return arbitrary shapes.
func writeError(w http.ResponseWriter, status int, body any) {
	if _, ok := body.(ProblemDetails); ok {
		w.Header().Set("Content-Type", "application/problem+json")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeResponse writes resp as protojson when it is a [proto.Message]; this
// is the expected path for any gRPC-generated response type. For non-proto
// values (e.g. mock responses in tests) it falls back to [encoding/json].
func writeResponse(w http.ResponseWriter, status int, resp any, marshal protojson.MarshalOptions) {
	w.Header().Set("Content-Type", "application/json")
	if pm, ok := resp.(proto.Message); ok {
		data, err := marshal.Marshal(pm)
		if err == nil {
			w.WriteHeader(status)
			_, _ = w.Write(data)
			return
		}
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
