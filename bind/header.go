package bind

import (
	"net/http"

	"github.com/HiveTraum/grpc-composition"
)

// Header binds a single HTTP request header into the request via the
// provided setter. Missing headers yield an empty string passed to the
// setter; the setter decides whether that is an error (required header)
// or means "leave the field at its zero value" (optional header).
//
// Header names are matched case-insensitively per the HTTP spec.
//
//	bind.Header("If-Match", func(req *pb.UpdateReq, v string) error {
//	    if v == "" {
//	        return fmt.Errorf("If-Match header is required")
//	    }
//	    req.Version = v
//	    return nil
//	})
func Header[Req any](name string, setter func(*Req, string) error) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		return setter(req, r.Header.Get(name))
	}
}

// HeaderString binds a string-valued HTTP header into a string field
// via an infallible setter. For string-to-string assignment there is no
// failure mode, so the setter does not return an error.
//
//	bind.HeaderString("X-Tenant-ID", func(req *pb.Req, v string) { req.TenantId = v })
//
// Use [Header] when you need to validate the value (e.g. required
// header policy) and surface a 400 with a descriptive message.
func HeaderString[Req any](name string, setter func(*Req, string)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		setter(req, r.Header.Get(name))
		return nil
	}
}
