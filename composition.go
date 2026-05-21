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

// Route is an [http.Handler] that proxies an HTTP request to a unary gRPC method.
type Route[Req, Resp any] struct {
	method  UnaryMethod[Req, Resp]
	binders []Binder[Req]
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
		method:  method,
		binders: binders,
	}
}

// ServeHTTP implements [http.Handler].
func (rt *Route[Req, Resp]) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req Req
	for _, b := range rt.binders {
		if err := b(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "bind: " + err.Error(),
			})
			return
		}
	}

	resp, err := rt.method(r.Context(), &req)
	if err != nil {
		status, body := DefaultErrorMapper(err)
		writeJSON(w, status, body)
		return
	}

	writeResponse(w, http.StatusOK, resp)
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
