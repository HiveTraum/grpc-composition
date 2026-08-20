package composition

import (
	"net/http"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrorMapper converts an error from a gRPC call into an HTTP status code
// and a response body. The default mapper produces a [ProblemDetails]
// value (RFC 7807); override via [SetDefaultErrorMapper] or
// [Route.WithErrorMapper].
type ErrorMapper func(err error) (int, any)

// ProblemDetails is the body produced by the default error mapper.
// It follows RFC 7807 with two gRPC-aware extension fields:
//
//   - Errors: populated from google.rpc.BadRequest.FieldViolations
//     attached to the gRPC status via status.WithDetails. Suitable for
//     surfacing per-field validation failures to the client.
//   - Reason / Metadata / Type: populated from google.rpc.ErrorInfo,
//     a structured error code with a stable reason string.
//
// When the framework writes a ProblemDetails value to an HTTP response,
// Content-Type is set to application/problem+json (per RFC 7807).
type ProblemDetails struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`

	// gRPC-aware extension members.
	Reason   string            `json:"reason,omitempty"`
	Errors   []FieldViolation  `json:"errors,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// FieldViolation describes a single per-field validation failure.
// Sourced from google.rpc.BadRequest.FieldViolation.
type FieldViolation struct {
	Field       string `json:"field"`
	Description string `json:"description"`
}

// toErrorMapper widens a mapper with a concrete body type into the
// [ErrorMapper] shape stored on a route. A nil mapper stays nil, so the
// route keeps falling back to [DefaultErrorMapper].
func toErrorMapper[Body any](fn func(error) (int, Body)) ErrorMapper {
	if fn == nil {
		return nil
	}
	return func(err error) (int, any) {
		return fn(err)
	}
}

var defaultErrorMapper ErrorMapper = rfc7807Mapper

// DefaultErrorMapper is the package-level error mapper used when no
// per-route mapper is set. By default it produces a [ProblemDetails]
// value derived from the gRPC status and any structured details
// attached via status.WithDetails.
//
// Replace via [SetDefaultErrorMapper]; per-route via [Route.WithErrorMapper].
func DefaultErrorMapper(err error) (int, any) {
	return defaultErrorMapper(err)
}

// SetDefaultErrorMapper replaces the package-level default mapper.
// Pass nil to restore the built-in RFC 7807 mapper.
//
// Intended to be called once at program startup; not safe for concurrent
// writes during request handling.
func SetDefaultErrorMapper(fn ErrorMapper) {
	if fn == nil {
		defaultErrorMapper = rfc7807Mapper
		return
	}
	defaultErrorMapper = fn
}

func rfc7807Mapper(err error) (int, any) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, ProblemDetails{
			Status: http.StatusInternalServerError,
			Title:  http.StatusText(http.StatusInternalServerError),
			Detail: "internal error",
		}
	}

	httpCode := codeToHTTP(st.Code())
	prob := ProblemDetails{
		Status: httpCode,
		Title:  http.StatusText(httpCode),
	}

	// 5xx responses redact both message and details so server-side
	// state does not leak to clients.
	if httpCode >= 500 {
		prob.Detail = "internal error"
		return httpCode, prob
	}

	prob.Detail = st.Message()
	populateDetails(&prob, st)
	return httpCode, prob
}

// populateDetails fills the gRPC-aware extension members of prob from the
// structured details attached to st via status.WithDetails.
func populateDetails(prob *ProblemDetails, st *status.Status) {
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *errdetails.BadRequest:
			for _, fv := range v.GetFieldViolations() {
				prob.Errors = append(prob.Errors, FieldViolation{
					Field:       fv.GetField(),
					Description: fv.GetDescription(),
				})
			}
		case *errdetails.ErrorInfo:
			prob.Reason = v.GetReason()
			if v.GetDomain() != "" && v.GetReason() != "" {
				prob.Type = v.GetDomain() + "/" + v.GetReason()
			}
			if md := v.GetMetadata(); len(md) > 0 {
				prob.Metadata = md
			}
		}
	}
}

// errorInfoOf returns the google.rpc.ErrorInfo attached to st, or nil.
func errorInfoOf(st *status.Status) *errdetails.ErrorInfo {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

// ReasonMapping declares the HTTP response for one google.rpc.ErrorInfo
// reason in a [MapReasons] table.
type ReasonMapping struct {
	// Status is the HTTP status code written when the reason matches.
	Status int
	// Detail optionally replaces the RFC 7807 detail member. When empty,
	// the upstream status message is used — redacted to "internal error"
	// if Status is 5xx, consistent with the default mapper.
	Detail string
}

// MapReasons returns an [ErrorMapper] that resolves errors through a
// declarative reason table before falling back to fallback (nil → the
// built-in RFC 7807 mapper).
//
// Internal services that distinguish domain errors by a machine-readable
// reason (google.rpc.ErrorInfo attached via status.WithDetails — the
// vocabulary is typically an enum in the service contract) let the
// composition layer declare its HTTP surface as a table instead of
// rebuilding the same errors.Is / switch chain in every handler:
//
//	vocab := composition.MapReasons(map[string]composition.ReasonMapping{
//	    "SERVICE_NAME_TAKEN":  {Status: http.StatusConflict, Detail: "service name already taken"},
//	    "REPO_HOST_UNAVAILABLE": {Status: http.StatusServiceUnavailable},
//	}, nil)
//
//	composition.SetDefaultErrorMapper(vocab) // globally
//	route.WithErrorMapper(vocab)             // or per route
//
// A matched reason produces a [ProblemDetails] with the table's status and
// the usual gRPC-aware members (reason, type, metadata, errors[]) — the
// reason is part of the declared API vocabulary, so structured members are
// not redacted even on 5xx; only the free-text detail falls back to
// "internal error" there. Errors without a status, without an ErrorInfo
// detail, or with a reason absent from the table go to the fallback mapper
// unchanged.
//
// The table is copied; later mutation of the argument has no effect.
func MapReasons(table map[string]ReasonMapping, fallback ErrorMapper) ErrorMapper {
	reasons := make(map[string]ReasonMapping, len(table))
	for reason, m := range table {
		reasons[reason] = m
	}
	if fallback == nil {
		fallback = rfc7807Mapper
	}
	return func(err error) (int, any) {
		st, ok := status.FromError(err)
		if !ok {
			return fallback(err)
		}
		info := errorInfoOf(st)
		if info == nil {
			return fallback(err)
		}
		m, hit := reasons[info.GetReason()]
		if !hit {
			return fallback(err)
		}

		prob := ProblemDetails{
			Status: m.Status,
			Title:  http.StatusText(m.Status),
		}
		switch {
		case m.Detail != "":
			prob.Detail = m.Detail
		case m.Status >= 500:
			prob.Detail = "internal error"
		default:
			prob.Detail = st.Message()
		}
		populateDetails(&prob, st)
		return m.Status, prob
	}
}

// codeToHTTP maps a gRPC status code to an HTTP status code.
func codeToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return 499 // client closed request (nginx convention)
	case codes.InvalidArgument, codes.OutOfRange:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists, codes.Aborted:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.FailedPrecondition:
		return http.StatusUnprocessableEntity
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Internal, codes.Unknown, codes.DataLoss:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
