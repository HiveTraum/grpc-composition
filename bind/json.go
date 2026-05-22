package bind

import (
	"io"
	"net/http"

	"github.com/HiveTraum/grpc-composition"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// protoPtr constrains PReq to be a pointer to Req that also satisfies
// [proto.Message]. The constraint is satisfied automatically by any
// pointer to a protoc-generated message type.
type protoPtr[Req any] interface {
	*Req
	proto.Message
}

// JSON parses the HTTP request body as protojson into a protobuf message.
//
// Req must be a generated protobuf message type. The constraint ensures
// *Req implements [proto.Message]. PReq is inferred by the compiler from Req
// and need not be specified at the call site.
//
// Example:
//
//	bind.JSON[pb.CreateUserRequest]()
//
// protojson is used (not encoding/json) so that well-known types
// (Timestamp, Duration, FieldMask), oneof variants and presence semantics
// are handled correctly.
func JSON[Req any, PReq protoPtr[Req]]() composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		return protojson.Unmarshal(body, PReq(req))
	}
}
