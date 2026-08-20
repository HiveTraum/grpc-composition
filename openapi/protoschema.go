package openapi

import (
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// protoSchema returns the schema of a proto message following protojson
// serialization rules under opts (UseProtoNames, UseEnumNumbers). Message
// schemas land in components keyed by the proto full name; the returned
// schema is a $ref.
func (g *generator) protoSchema(m proto.Message, opts protojson.MarshalOptions) *Schema {
	return g.messageRef(m.ProtoReflect().Descriptor(), opts)
}

func (g *generator) messageRef(md protoreflect.MessageDescriptor, opts protojson.MarshalOptions) *Schema {
	if s := wellKnownSchema(md); s != nil {
		return s
	}
	name := string(md.FullName())
	if _, ok := g.schemas[name]; !ok {
		// Placeholder first: self-referential messages recurse into
		// messageRef and must find the name registered to get a $ref
		// instead of infinite recursion.
		g.schemas[name] = &Schema{}
		g.schemas[name] = g.messageSchema(md, opts)
	}
	return &Schema{Ref: "#/components/schemas/" + name}
}

func (g *generator) messageSchema(md protoreflect.MessageDescriptor, opts protojson.MarshalOptions) *Schema {
	s := &Schema{Type: "object", Properties: make(map[string]*Schema)}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := fd.JSONName()
		if opts.UseProtoNames {
			name = fd.TextName()
		}
		s.Properties[name] = g.fieldSchema(fd, opts)
	}
	return s
}

func (g *generator) fieldSchema(fd protoreflect.FieldDescriptor, opts protojson.MarshalOptions) *Schema {
	if fd.IsMap() {
		// protojson renders maps as JSON objects; keys are always
		// strings on the wire.
		return &Schema{Type: "object", AdditionalProperties: g.singularSchema(fd.MapValue(), opts)}
	}
	s := g.singularSchema(fd, opts)
	if fd.IsList() {
		return &Schema{Type: "array", Items: s}
	}
	return s
}

func (g *generator) singularSchema(fd protoreflect.FieldDescriptor, opts protojson.MarshalOptions) *Schema {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return &Schema{Type: "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return &Schema{Type: "integer", Format: "int32"}
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return &Schema{Type: "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// protojson emits 64-bit integers as JSON strings.
		return &Schema{Type: "string", Format: "int64"}
	case protoreflect.FloatKind:
		return &Schema{Type: "number", Format: "float"}
	case protoreflect.DoubleKind:
		return &Schema{Type: "number", Format: "double"}
	case protoreflect.StringKind:
		return &Schema{Type: "string"}
	case protoreflect.BytesKind:
		return &Schema{Type: "string", Format: "byte"}
	case protoreflect.EnumKind:
		if opts.UseEnumNumbers {
			return &Schema{Type: "integer", Format: "int32"}
		}
		return &Schema{Type: "string", Enum: enumValueNames(fd.Enum())}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return g.messageRef(fd.Message(), opts)
	default:
		return &Schema{}
	}
}

func enumValueNames(desc protoreflect.EnumDescriptor) []string {
	vals := desc.Values()
	names := make([]string, vals.Len())
	for i := range names {
		names[i] = string(vals.Get(i).Name())
	}
	return names
}

// wellKnownSchema maps the well-known types that protojson serializes
// specially (not as plain objects). Returns nil for ordinary messages.
func wellKnownSchema(md protoreflect.MessageDescriptor) *Schema {
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return &Schema{Type: "string", Format: "date-time"}
	case "google.protobuf.Duration":
		return &Schema{Type: "string"}
	case "google.protobuf.StringValue":
		return &Schema{Type: "string"}
	case "google.protobuf.BytesValue":
		return &Schema{Type: "string", Format: "byte"}
	case "google.protobuf.BoolValue":
		return &Schema{Type: "boolean"}
	case "google.protobuf.Int32Value":
		return &Schema{Type: "integer", Format: "int32"}
	case "google.protobuf.UInt32Value":
		return &Schema{Type: "integer"}
	case "google.protobuf.Int64Value", "google.protobuf.UInt64Value":
		return &Schema{Type: "string", Format: "int64"}
	case "google.protobuf.FloatValue":
		return &Schema{Type: "number", Format: "float"}
	case "google.protobuf.DoubleValue":
		return &Schema{Type: "number", Format: "double"}
	case "google.protobuf.Struct":
		return &Schema{Type: "object"}
	case "google.protobuf.Value":
		return &Schema{} // any JSON value
	case "google.protobuf.ListValue":
		return &Schema{Type: "array", Items: &Schema{}}
	case "google.protobuf.FieldMask":
		return &Schema{Type: "string"}
	case "google.protobuf.Empty", "google.protobuf.Any":
		return &Schema{Type: "object"}
	default:
		return nil
	}
}
