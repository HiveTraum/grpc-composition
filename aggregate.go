package composition

import (
	"context"
	"net/http"
)

// AggregateFunc is the handler shape accepted by [Aggregate]. It receives
// the request context (already populated with outgoing gRPC metadata
// from [App.Handler] if one is wrapping the router) and the raw
// [http.Request]. It returns either a value to serialize as the response
// body or an error.
type AggregateFunc func(ctx context.Context, r *http.Request) (any, error)

// AggregateRoute is an [http.Handler] that wraps a custom aggregation
// handler with the framework's uniform error mapping and response
// serialization. Constructed via [Aggregate].
type AggregateRoute struct {
	fn            AggregateFunc
	successStatus int
	errorMapper   ErrorMapper // nil → package-level DefaultErrorMapper
}

// Aggregate wraps a custom handler with the same error mapping
// (RFC 7807 / [DefaultErrorMapper]) and response serialization
// (protojson for [proto.Message] values, encoding/json otherwise) used
// by [Proxy]. Use it for endpoints that call multiple gRPC services and
// assemble a combined response — Proxy doesn't fit because there is no
// one-to-one HTTP↔gRPC mapping.
//
// The framework deliberately does not ship parallelism primitives:
// errgroup, sourcegraph/conc, samber/ro and friends already cover that
// space well. Inside the handler, use whichever concurrency model fits
// your codebase.
//
//	mux.Handle("GET /feed/{user_id}", composition.Aggregate(
//	    func(ctx context.Context, r *http.Request) (any, error) {
//	        uid := r.PathValue("user_id")
//
//	        g, gctx := errgroup.WithContext(ctx)
//	        var user *pb.User
//	        var posts *pb.ListPostsResponse
//	        g.Go(func() error {
//	            u, err := users.GetUser(gctx, &pb.GetUserRequest{Id: uid})
//	            if err != nil { return err }
//	            user = u
//	            return nil
//	        })
//	        g.Go(func() error {
//	            p, err := posts.ListPosts(gctx, &pb.ListPostsRequest{UserId: uid})
//	            if err != nil { return err }
//	            posts = p
//	            return nil
//	        })
//	        if err := g.Wait(); err != nil {
//	            return nil, err
//	        }
//
//	        return FeedResponse{User: user, Posts: posts.Posts}, nil
//	    },
//	))
func Aggregate(fn AggregateFunc) *AggregateRoute {
	return &AggregateRoute{
		fn:            fn,
		successStatus: http.StatusOK,
	}
}

// OnSuccess overrides the HTTP status code written on a successful
// aggregation. The default is 200 OK.
func (rt *AggregateRoute) OnSuccess(status int) *AggregateRoute {
	rt.successStatus = status
	return rt
}

// WithErrorMapper overrides the package-level [DefaultErrorMapper] for
// this aggregation only. Mirrors [Route.WithErrorMapper], including the
// inferred error-body type Body.
func (rt *AggregateRoute) WithErrorMapper[Body any](fn func(error) (int, Body)) *AggregateRoute {
	rt.errorMapper = toErrorMapper(fn)
	return rt
}

// ServeHTTP implements [http.Handler].
func (rt *AggregateRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := rt.fn(r.Context(), r)
	if err != nil {
		mapper := rt.errorMapper
		if mapper == nil {
			mapper = DefaultErrorMapper
		}
		status, mappedBody := mapper(err)
		writeError(w, status, mappedBody)
		return
	}
	writeResponse(w, rt.successStatus, body)
}
