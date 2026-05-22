// Package bind provides Binder constructors for grpc-composition.
//
// All binders depend only on the standard library net/http and the parent
// composition package. They do not import any third-party router.
package bind

import (
	"net/http"

	"github.com/HiveTraum/grpc-composition"
)

// Path binds a single URL path parameter into the request via the provided
// setter. It calls [composition.PathParam] under the hood, which respects
// any extractor installed via [composition.SetDefaultPathExtractor].
//
// The setter receives the raw string value from the path. If the field on
// the request is not a string (int, UUID, time, etc.), parse it inside the
// setter and return the error — it surfaces to the client as HTTP 400.
//
// Example (string field, infallible):
//
//	bind.Path("id", func(req *pb.GetUserRequest, v string) error {
//	    req.Id = v
//	    return nil
//	})
//
// Example (parsed int field):
//
//	bind.Path("page", func(req *pb.ListReq, v string) error {
//	    n, err := strconv.Atoi(v)
//	    if err != nil {
//	        return fmt.Errorf("page: %w", err)
//	    }
//	    req.Page = int32(n)
//	    return nil
//	})
func Path[Req any](name string, setter func(*Req, string) error) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		return setter(req, composition.PathParam(r, name))
	}
}
