package composition

import (
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DefaultErrorMapper maps a gRPC error to an HTTP status code and a JSON body.
//
// 5xx responses do not leak the upstream status message: the body contains a
// generic "internal error" string. Tests should assert on this redaction.
func DefaultErrorMapper(err error) (int, any) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, map[string]string{
			"error": "internal error",
		}
	}
	httpCode := codeToHTTP(st.Code())
	if httpCode >= 500 {
		return httpCode, map[string]string{"error": "internal error"}
	}
	return httpCode, map[string]string{"error": st.Message()}
}

// codeToHTTP maps a gRPC status code to an HTTP status code.
// Table follows the conventions outlined in README.md.
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
