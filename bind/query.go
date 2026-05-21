package bind

import (
	"net/http"

	"github.com/traum-tech/grpc-composition"
)

// Query binds a single URL query parameter into the request via the provided
// setter. Missing parameters yield an empty string; the setter is responsible
// for parsing and validation.
//
// Example:
//
//	bind.Query("limit", func(req *pb.ListReq, v string) {
//	    n, _ := strconv.Atoi(v)
//	    req.Limit = int32(n)
//	})
func Query[Req any](name string, setter func(*Req, string)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		setter(req, r.URL.Query().Get(name))
		return nil
	}
}
