package bind

import (
	"encoding/json"
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

// BodyJSON parses the HTTP request body as protojson directly into the
// protobuf request message. Use when the REST body shape matches the
// proto shape one-to-one — most common case.
//
// Req must be a generated protobuf message type. PReq is inferred from
// Req and does not need to be specified at the call site.
//
// Example:
//
//	bind.BodyJSON[pb.CreateUserRequest]()
//
// protojson is used (not encoding/json) so well-known types (Timestamp,
// Duration, FieldMask), oneof variants and presence semantics are
// handled correctly.
func BodyJSON[Req any, PReq protoPtr[Req]]() composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		return protojson.Unmarshal(body, PReq(req))
	}
}

// BodyJSONInto decodes the HTTP request body as JSON into a user-defined
// DTO and then invokes apply to map the DTO into the protobuf request.
// Use when the REST body shape differs from the proto shape and the
// mapping can fail (parsing typed fields, validation, lookups).
//
// Errors returned by apply surface as HTTP 400.
//
// Example:
//
//	type CreateUserDTO struct {
//	    FullName string `json:"full_name"`
//	    Email    string `json:"email"`
//	}
//
//	bind.BodyJSONInto(func(dto CreateUserDTO, req *pb.CreateUserRequest) error {
//	    parts := strings.SplitN(dto.FullName, " ", 2)
//	    if len(parts) < 2 {
//	        return fmt.Errorf("full_name must contain first and last name")
//	    }
//	    req.GivenName, req.FamilyName = parts[0], parts[1]
//	    req.Email = dto.Email
//	    return nil
//	})
func BodyJSONInto[Req any, DTO any](apply func(DTO, *Req) error) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		var dto DTO
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			return err
		}
		return apply(dto, req)
	}
}

// BodyJSONMap is the infallible variant of [BodyJSONInto]: apply is a
// pure field-copy function with no error return. Use when the mapping is
// just moving fields between two structs, possibly with renames or
// trivial transforms.
//
// Example:
//
//	bind.BodyJSONMap(func(dto CreateUserDTO, req *pb.CreateUserRequest) {
//	    req.Name = dto.FullName
//	    req.Email = dto.Email
//	})
//
// For mappings that can fail, use [BodyJSONInto] instead.
func BodyJSONMap[Req any, DTO any](apply func(DTO, *Req)) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		var dto DTO
		if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
			return err
		}
		apply(dto, req)
		return nil
	}
}

// Body reads the raw HTTP request body and hands it to a user-supplied
// parse function. Use for non-JSON formats (YAML, raw protobuf wire,
// form-encoded data, etc.) where none of the Body* helpers fit.
//
// Example (raw protobuf wire format):
//
//	bind.Body(func(data []byte, req *pb.Req) error {
//	    return proto.Unmarshal(data, req)
//	})
//
// Example (YAML, using gopkg.in/yaml.v3):
//
//	bind.Body(func(data []byte, req *pb.Req) error {
//	    return yaml.Unmarshal(data, req)
//	})
func Body[Req any](parse func([]byte, *Req) error) composition.Binder[Req] {
	return func(r *http.Request, req *Req) error {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return err
		}
		return parse(data, req)
	}
}
