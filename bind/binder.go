package bind

import (
	"net/http"

	"github.com/HiveTraum/grpc-composition"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// paramBinder is the concrete [composition.Binder] behind every
// parameter-consuming constructor in this package: the binding closure
// plus the metadata exposed via [composition.ParamDocumenter] for
// documentation generation.
type paramBinder[Req any] struct {
	fn   func(*http.Request, *Req) error
	spec composition.ParamSpec
}

func (b paramBinder[Req]) Bind(r *http.Request, req *Req) error {
	return b.fn(r, req)
}

func (b paramBinder[Req]) ParamSpec() composition.ParamSpec {
	return b.spec
}

// bodyBinder is the concrete [composition.Binder] behind the Body*
// constructors, exposing the body schema via [composition.BodyDocumenter].
type bodyBinder[Req any] struct {
	fn   func(*http.Request, *Req) error
	spec composition.BodySpec
}

func (b bodyBinder[Req]) Bind(r *http.Request, req *Req) error {
	return b.fn(r, req)
}

func (b bodyBinder[Req]) BodySpec() composition.BodySpec {
	return b.spec
}

func pathSpec(name, typ, format string) composition.ParamSpec {
	return composition.ParamSpec{In: composition.InPath, Name: name, Type: typ, Format: format, Required: true}
}

func querySpec(name, typ, format string) composition.ParamSpec {
	return composition.ParamSpec{In: composition.InQuery, Name: name, Type: typ, Format: format}
}

func headerSpec(name, typ, format string) composition.ParamSpec {
	return composition.ParamSpec{In: composition.InHeader, Name: name, Type: typ, Format: format}
}

// enumValueNames lists the canonical proto names of an enum, for the
// parameter spec of the *Enum binders.
func enumValueNames(desc protoreflect.EnumDescriptor) []string {
	vals := desc.Values()
	names := make([]string, vals.Len())
	for i := range names {
		names[i] = string(vals.Get(i).Name())
	}
	return names
}
