package bind

import (
	"context"
	"fmt"
	"net/http"

	"github.com/HiveTraum/grpc-composition"
)

// Ctx binds a value derived from the request context into the request
// message. Use it to carry identity established by upstream middleware —
// the authenticated user, tenant or organization id — into a proto
// request field without hand-writing a [composition.Binder]:
//
//	bind.Ctx(
//	    func(ctx context.Context) (string, error) {
//	        org, ok := authn.OrganizationID(ctx)
//	        if !ok {
//	            return "", errors.New("no organization in context")
//	        }
//	        return org, nil
//	    },
//	    func(req *pb.ListServicesRequest, v string) { req.OrganizationId = v },
//	)
//
// An error from get surfaces as HTTP 400 like any binder error, wrapped
// with a "context: " prefix. A failing get usually indicates a wiring bug
// — the middleware that should have populated (or rejected) the request
// did not run — so prefer rejecting unauthenticated requests in
// middleware and treating Ctx as infallible plumbing.
func Ctx[Req any, V any](get func(ctx context.Context) (V, error), setter func(*Req, V)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		v, err := get(r.Context())
		if err != nil {
			return fmt.Errorf("context: %w", err)
		}
		setter(req, v)
		return nil
	}
}
