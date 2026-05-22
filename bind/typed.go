package bind

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/traum-tech/grpc-composition"
)

// PathAs binds a path parameter parsed via the supplied function.
//
// The path value is treated as REQUIRED: an empty string is passed to
// parse, which typically yields an error and surfaces as HTTP 400. Errors
// returned by parse are wrapped with the parameter name prefix.
//
// Use this for non-string types not covered by the typed sugar helpers
// (e.g. UUID, time, custom enums):
//
//	bind.PathAs("user_id", uuid.Parse, func(req *pb.Req, v uuid.UUID) {
//	    req.UserId = v.String()
//	})
func PathAs[Req any, T any](
	name string,
	parse func(string) (T, error),
	setter func(*Req, T),
) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		v, err := parse(composition.PathParam(r, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		setter(req, v)
		return nil
	}
}

// QueryAs binds a query parameter parsed via the supplied function.
//
// Missing (empty) values are treated as "not provided": parse is NOT
// called, the setter is NOT invoked, and the field is left at its zero
// value. Parse errors surface as HTTP 400 with the parameter name prefix.
func QueryAs[Req any, T any](
	name string,
	parse func(string) (T, error),
	setter func(*Req, T),
) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			return nil
		}
		v, err := parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		setter(req, v)
		return nil
	}
}

// parseInt32 / parseInt64 / strconv.ParseBool back the typed sugar below.

func parseInt32(s string) (int32, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	return int32(n), err
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// ===== Sugar for common proto scalar types =====

// PathInt32 binds a required path parameter parsed as int32.
//
//	bind.PathInt32("page", func(req *pb.ListReq, v int32) { req.Page = v })
func PathInt32[Req any](name string, setter func(*Req, int32)) composition.Binder[Req] {
	return PathAs(name, parseInt32, setter)
}

// PathInt64 binds a required path parameter parsed as int64.
func PathInt64[Req any](name string, setter func(*Req, int64)) composition.Binder[Req] {
	return PathAs(name, parseInt64, setter)
}

// PathBool binds a required path parameter parsed as bool.
//
// Accepts the values recognized by [strconv.ParseBool]:
// "1", "t", "T", "TRUE", "true", "True" → true;
// "0", "f", "F", "FALSE", "false", "False" → false.
func PathBool[Req any](name string, setter func(*Req, bool)) composition.Binder[Req] {
	return PathAs(name, strconv.ParseBool, setter)
}

// QueryInt32 binds an optional query parameter parsed as int32.
// Missing parameter leaves the field at zero.
//
//	bind.QueryInt32("limit", func(req *pb.ListReq, v int32) { req.Limit = v })
func QueryInt32[Req any](name string, setter func(*Req, int32)) composition.Binder[Req] {
	return QueryAs(name, parseInt32, setter)
}

// QueryInt64 binds an optional query parameter parsed as int64.
func QueryInt64[Req any](name string, setter func(*Req, int64)) composition.Binder[Req] {
	return QueryAs(name, parseInt64, setter)
}

// QueryBool binds an optional query parameter parsed as bool.
func QueryBool[Req any](name string, setter func(*Req, bool)) composition.Binder[Req] {
	return QueryAs(name, strconv.ParseBool, setter)
}
