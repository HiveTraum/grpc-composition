package bind

import (
	"net/http"

	"github.com/HiveTraum/grpc-composition"
)

// Query binds a single URL query parameter into the request via the provided
// setter. Missing parameters yield an empty string passed to the setter; the
// setter decides whether that is an error (required param) or means "leave
// the field at its zero value" (optional param).
//
// As with [Path], parsing into non-string fields is done inside the setter;
// returned errors surface as HTTP 400.
//
// Example (optional int query):
//
//	bind.Query("limit", func(req *pb.ListReq, v string) error {
//	    if v == "" {
//	        return nil // not provided → leave Limit at 0
//	    }
//	    n, err := strconv.Atoi(v)
//	    if err != nil {
//	        return fmt.Errorf("limit: %w", err)
//	    }
//	    req.Limit = int32(n)
//	    return nil
//	})
func Query[Req any](name string, setter func(*Req, string) error) composition.Binder[Req] {
	return paramBinder[Req]{
		fn: func(r *http.Request, req *Req) error {
			return setter(req, r.URL.Query().Get(name))
		},
		spec: querySpec(name, "string", ""),
	}
}
