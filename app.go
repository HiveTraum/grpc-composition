package composition

import (
	"net/http"
	"strings"

	"google.golang.org/grpc/metadata"
)

// App carries cross-cutting concerns that apply uniformly to all routes
// in an HTTP server. The current responsibility is HTTP→gRPC metadata
// forwarding: copying selected HTTP request headers into the outgoing
// gRPC metadata so backend services see them.
//
// Use [App.Handler] to wrap an [http.Handler] with the configured
// behavior.
//
// All concerns live on App rather than per-route because they should
// apply uniformly across the service — endpoint-specific exceptions in
// this area usually indicate a bug, not a feature.
type App struct {
	metadataForward []string // canonical lowercase header names
}

// AppOption configures an [App] at construction time.
type AppOption func(*App)

// New constructs an [App] with the given options.
func New(opts ...AppOption) *App {
	a := &App{}
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

// Handler wraps inner with the App's behavior. For each request it
// constructs an outgoing gRPC metadata context from the allowlisted
// headers, so any [Proxy] handler downstream sees them as outgoing
// metadata on its gRPC call.
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
	forward := a.metadataForward
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		md := metadata.New(nil)
		for _, h := range forward {
			for _, v := range r.Header.Values(h) {
				md.Append(h, v)
			}
		}
		ctx := r.Context()
		if len(md) > 0 {
			ctx = metadata.NewOutgoingContext(ctx, md)
		}
		inner.ServeHTTP(w, r.WithContext(ctx))
	})
}
