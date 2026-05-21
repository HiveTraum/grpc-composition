// Package bind provides Binder constructors for grpc-composition.
//
// All binders depend only on the standard library net/http and the parent
// composition package. They do not import any third-party router.
package bind

import (
	"net/http"

	"github.com/traum-tech/grpc-composition"
)

// Path binds a single URL path parameter into the request via the provided
// setter. It uses [http.Request.PathValue] (Go 1.22+) under the hood.
//
// Example:
//
//	bind.Path("id", func(req *pb.GetUserRequest, v string) { req.Id = v })
func Path[Req any](name string, setter func(*Req, string)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		setter(req, r.PathValue(name))
		return nil
	}
}
