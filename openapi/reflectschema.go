package openapi

import (
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeFor[time.Time]()

// dtoSchema returns the schema of a Go value serialized with
// encoding/json — the path taken by Map DTOs and BodyJSONInto /
// BodyJSONMap bodies. Named struct types land in components keyed by
// their Go type name ("pkg.Name"); anonymous structs are inlined.
func (g *generator) dtoSchema(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.Pointer:
		return g.dtoSchema(t.Elem())
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Int64:
		return &Schema{Type: "integer", Format: "int64"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// []byte marshals as base64.
			return &Schema{Type: "string", Format: "byte"}
		}
		return &Schema{Type: "array", Items: g.dtoSchema(t.Elem())}
	case reflect.Map:
		return &Schema{Type: "object", AdditionalProperties: g.dtoSchema(t.Elem())}
	case reflect.Interface:
		return &Schema{} // any JSON value
	case reflect.Struct:
		if t == timeType {
			return &Schema{Type: "string", Format: "date-time"}
		}
		return g.structRef(t)
	default:
		return &Schema{}
	}
}

func (g *generator) structRef(t reflect.Type) *Schema {
	if t.Name() == "" {
		return g.structSchema(t) // anonymous struct — inline
	}
	name := t.String() // "pkg.Name"
	if _, ok := g.schemas[name]; !ok {
		g.schemas[name] = &Schema{} // placeholder breaks cycles
		g.schemas[name] = g.structSchema(t)
	}
	return &Schema{Ref: "#/components/schemas/" + name}
}

func (g *generator) structSchema(t reflect.Type) *Schema {
	s := &Schema{Type: "object", Properties: make(map[string]*Schema)}
	g.addStructFields(s, t)
	return s
}

// addStructFields fills properties following encoding/json field rules:
// exported fields only, json tag names, "-" skipped, untagged embedded
// structs flattened.
func (g *generator) addStructFields(s *Schema, t reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" && tag == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				g.addStructFields(s, ft)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		s.Properties[name] = g.dtoSchema(f.Type)
	}
}
