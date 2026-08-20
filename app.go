package composition

import (
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// App owns the router and the cross-cutting concerns that apply
// uniformly to every route in an HTTP server. Routes are registered with
// the verb methods ([App.Get], [App.Post], …), which bind a URL pattern
// to a unary gRPC method; App itself is an [http.Handler]:
//
//	app := composition.New(
//	    composition.WithMetadataForward("authorization"),
//	)
//	app.Get("/users/{id}", users.GetUser,
//	    bind.PathString("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
//	)
//	http.ListenAndServe(":8080", app)
//
// The current cross-cutting concern is HTTP→gRPC metadata forwarding:
// copying selected HTTP request headers into the outgoing gRPC metadata
// so backend services see them. Such concerns live on App rather than
// per-route because they should apply uniformly across the service —
// endpoint-specific exceptions in this area usually indicate a bug, not
// a feature.
//
// To keep an existing router instead, register routes on it with the
// package-level [Proxy] and wrap it with [App.Handler].
type App struct {
	mux             *http.ServeMux
	metadataForward []string // canonical lowercase header names
}

// AppOption configures an [App] at construction time.
type AppOption func(*App)

// New constructs an [App] with the given options.
func New(opts ...AppOption) *App {
	a := &App{mux: http.NewServeMux()}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// WithMetadataForward declares which HTTP request headers should be
// copied into outgoing gRPC metadata. Header names are matched
// case-insensitively. There is no wildcard — pass an explicit
// allowlist to avoid leaking unintended headers downstream.
//
// Multi-value headers preserve all values.
//
//	composition.New(
//	    composition.WithMetadataForward("authorization", "x-request-id"),
//	)
func WithMetadataForward(headers ...string) AppOption {
	return func(a *App) {
		for _, h := range headers {
			a.metadataForward = append(a.metadataForward, strings.ToLower(h))
		}
	}
}

// Get registers a proxy route for GET pattern on the App's router and
// returns it, so route-level options can be chained:
//
//	app.Get("/users/{id}", users.GetUser,
//	    bind.PathString("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
//	).Map(func(u *pb.User) UserDTO { return toDTO(u) })
//
// Req and Resp are inferred from the method value, exactly as with the
// package-level [Proxy].
//
// pattern follows [http.ServeMux] syntax without the verb — it is
// prepended by the method. Registering the same verb and pattern twice
// panics, as it does with a plain [http.ServeMux].
func (a *App) Get[Req, Resp any](pattern string, method UnaryMethod[Req, Resp], binders ...Binder[Req]) *Route[Req, Resp] {
	return a.handleVerb(http.MethodGet, pattern, method, binders)
}

// Post registers a proxy route for POST pattern. See [App.Get].
func (a *App) Post[Req, Resp any](pattern string, method UnaryMethod[Req, Resp], binders ...Binder[Req]) *Route[Req, Resp] {
	return a.handleVerb(http.MethodPost, pattern, method, binders)
}

// Put registers a proxy route for PUT pattern. See [App.Get].
func (a *App) Put[Req, Resp any](pattern string, method UnaryMethod[Req, Resp], binders ...Binder[Req]) *Route[Req, Resp] {
	return a.handleVerb(http.MethodPut, pattern, method, binders)
}

// Patch registers a proxy route for PATCH pattern. See [App.Get].
func (a *App) Patch[Req, Resp any](pattern string, method UnaryMethod[Req, Resp], binders ...Binder[Req]) *Route[Req, Resp] {
	return a.handleVerb(http.MethodPatch, pattern, method, binders)
}

// Delete registers a proxy route for DELETE pattern. See [App.Get].
func (a *App) Delete[Req, Resp any](pattern string, method UnaryMethod[Req, Resp], binders ...Binder[Req]) *Route[Req, Resp] {
	return a.handleVerb(http.MethodDelete, pattern, method, binders)
}

func (a *App) handleVerb[Req, Resp any](verb, pattern string, method UnaryMethod[Req, Resp], binders []Binder[Req]) *Route[Req, Resp] {
	rt := Proxy(method, binders...)
	a.mux.Handle(verb+" "+pattern, rt)
	return rt
}

// Handle registers an arbitrary [http.Handler] on the App's router.
// Use it for [Aggregate] endpoints and for anything that is not a
// one-to-one proxy; pattern carries the verb itself, as [http.ServeMux]
// expects.
//
//	app.Handle("GET /feed/{user_id}", composition.Aggregate(feed))
func (a *App) Handle(pattern string, h http.Handler) {
	a.mux.Handle(pattern, h)
}

// ServeHTTP implements [http.Handler]: it applies the App's cross-cutting
// concerns and dispatches to the App's own router.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, a.forwardMetadata(r))
}

// Handler wraps inner with the App's behavior. For each request it
// constructs an outgoing gRPC metadata context from the allowlisted
// headers, so any [Proxy] handler downstream sees them as outgoing
// metadata on its gRPC call.
//
// Use it to keep an existing router; routes registered on the App itself
// go through the same path via [App.ServeHTTP].
//
// If no metadata forwarding is configured, inner is returned unchanged.
//
//	r := http.NewServeMux()
//	r.Handle("GET /users/{id}", composition.Proxy(users.GetUser, ...))
//	http.ListenAndServe(":8080", app.Handler(r))
func (a *App) Handler(inner http.Handler) http.Handler {
	if len(a.metadataForward) == 0 {
		return inner
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, a.forwardMetadata(r))
	})
}

// forwardMetadata returns r with the allowlisted headers attached as
// outgoing gRPC metadata, or r unchanged when there is nothing to carry.
func (a *App) forwardMetadata(r *http.Request) *http.Request {
	if len(a.metadataForward) == 0 {
		return r
	}
	md := metadata.New(nil)
	for _, h := range a.metadataForward {
		for _, v := range r.Header.Values(h) {
			md.Append(h, v)
		}
	}
	if len(md) == 0 {
		return r
	}
	return r.WithContext(metadata.NewOutgoingContext(r.Context(), md))
}
