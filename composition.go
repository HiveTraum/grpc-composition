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
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Binder populates a typed request value from an [*http.Request].
//
// Binder constructors live in the bind/ subpackage.
type Binder[Req any] func(r *http.Request, req *Req) error

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

// Route is an [http.Handler] that proxies an HTTP request to a unary gRPC method.
type Route[Req, Resp any] struct {
	method        UnaryMethod[Req, Resp]
	binders       []Binder[Req]
	mapper        func(*Resp) any
	successStatus int
	errorMapper   ErrorMapper // nil → use package-level DefaultErrorMapper
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
// The returned value is serialized via encoding/json — protojson semantics
// no longer apply once the proto type is left behind.
func (rt *Route[Req, Resp]) Map(fn func(*Resp) any) *Route[Req, Resp] {
	rt.mapper = fn
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
//	route.WithErrorMapper(func(err error) (int, any) {
//	    return 500, map[string]string{"code": "X-LEGACY"}
//	})
func (rt *Route[Req, Resp]) WithErrorMapper(fn ErrorMapper) *Route[Req, Resp] {
	rt.errorMapper = fn
	return rt
}

// ServeHTTP implements [http.Handler].
func (rt *Route[Req, Resp]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req Req
	for _, b := range rt.binders {
		if err := b(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, ProblemDetails{
				Status: http.StatusBadRequest,
				Title:  http.StatusText(http.StatusBadRequest),
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
	writeResponse(w, rt.successStatus, resp)
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
func writeResponse(w http.ResponseWriter, status int, resp any) {
	w.Header().Set("Content-Type", "application/json")
	if pm, ok := resp.(proto.Message); ok {
		data, err := protojson.Marshal(pm)
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
