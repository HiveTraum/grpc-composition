package bind

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/HiveTraum/grpc-composition"
	"google.golang.org/protobuf/reflect/protoreflect"
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

// HeaderAs binds an HTTP request header parsed via the supplied function.
// Missing (empty) headers are treated as "not provided": parse is NOT
// called, the setter is NOT invoked, and the field is left at its zero
// value. Parse errors surface as HTTP 400 with the header name prefix.
//
// Use for non-string types not covered by the typed sugar helpers.
func HeaderAs[Req any, T any](
	name string,
	parse func(string) (T, error),
	setter func(*Req, T),
) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		raw := r.Header.Get(name)
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

func parseFloat64(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// ===== Sugar for common proto scalar types =====

// PathString binds a path parameter as a string field. Unlike [Path],
// the setter does not return an error — assigning a string can never
// fail, so the explicit `return nil` is omitted.
//
//	bind.PathString("id", func(req *pb.GetUserRequest, v string) { req.Id = v })
//
// Use [Path] instead when you need to validate the string and surface
// the validation error as HTTP 400.
func PathString[Req any](name string, setter func(*Req, string)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		setter(req, composition.PathParam(r, name))
		return nil
	}
}

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

// PathFloat64 binds a required path parameter parsed as float64.
func PathFloat64[Req any](name string, setter func(*Req, float64)) composition.Binder[Req] {
	return PathAs(name, parseFloat64, setter)
}

// QueryString binds a query parameter as a string field. Missing values
// pass an empty string to the setter (consistent with how protobuf
// represents an absent string). The setter does not return an error.
//
//	bind.QueryString("name", func(req *pb.SearchReq, v string) { req.Name = v })
//
// Use [Query] instead when you need to validate the string.
func QueryString[Req any](name string, setter func(*Req, string)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		setter(req, r.URL.Query().Get(name))
		return nil
	}
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

// QueryFloat64 binds an optional query parameter parsed as float64.
func QueryFloat64[Req any](name string, setter func(*Req, float64)) composition.Binder[Req] {
	return QueryAs(name, parseFloat64, setter)
}

// HeaderInt32 binds an optional HTTP header parsed as int32.
// Missing header leaves the field at zero.
func HeaderInt32[Req any](name string, setter func(*Req, int32)) composition.Binder[Req] {
	return HeaderAs(name, parseInt32, setter)
}

// HeaderInt64 binds an optional HTTP header parsed as int64.
func HeaderInt64[Req any](name string, setter func(*Req, int64)) composition.Binder[Req] {
	return HeaderAs(name, parseInt64, setter)
}

// HeaderBool binds an optional HTTP header parsed as bool.
func HeaderBool[Req any](name string, setter func(*Req, bool)) composition.Binder[Req] {
	return HeaderAs(name, strconv.ParseBool, setter)
}

// HeaderFloat64 binds an optional HTTP header parsed as float64.
func HeaderFloat64[Req any](name string, setter func(*Req, float64)) composition.Binder[Req] {
	return HeaderAs(name, parseFloat64, setter)
}

// ===== Enum binders =====

// protoEnum constrains T to a protoc-generated enum: an int32-based type
// that satisfies [protoreflect.Enum]. All standard generated enums qualify.
type protoEnum interface {
	~int32
	protoreflect.Enum
}

// PathEnum binds a path parameter to a protobuf enum field.
//
// The parameter is matched against the enum's canonical proto names
// (case-sensitive, e.g. "STATUS_ACTIVE") or its underlying numeric
// value (e.g. "1"). Unknown values yield HTTP 400.
//
//	bind.PathEnum("status", func(req *pb.Req, v pb.Status) { req.Status = v })
//
// Case-insensitive or alias-based matching is intentionally not built in:
// use [PathAs] with a custom parser if you need looser semantics.
func PathEnum[Req any, T protoEnum](name string, setter func(*Req, T)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		var zero T
		n, err := lookupEnum(zero.Descriptor(), composition.PathParam(r, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		setter(req, T(n))
		return nil
	}
}

// QueryEnum binds an optional query parameter to a protobuf enum field.
// A missing or empty parameter leaves the field at its zero value
// (typically the *_UNSPECIFIED variant). Otherwise the same matching
// rules as [PathEnum] apply.
//
//	bind.QueryEnum("role", func(req *pb.Req, v pb.Role) { req.Role = v })
func QueryEnum[Req any, T protoEnum](name string, setter func(*Req, T)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		raw := r.URL.Query().Get(name)
		if raw == "" {
			return nil
		}
		var zero T
		n, err := lookupEnum(zero.Descriptor(), raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		setter(req, T(n))
		return nil
	}
}

func lookupEnum(desc protoreflect.EnumDescriptor, raw string) (protoreflect.EnumNumber, error) {
	if v := desc.Values().ByName(protoreflect.Name(raw)); v != nil {
		return v.Number(), nil
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if v := desc.Values().ByNumber(protoreflect.EnumNumber(n)); v != nil {
			return v.Number(), nil
		}
	}
	return 0, fmt.Errorf("%q is not a valid enum value", raw)
}
