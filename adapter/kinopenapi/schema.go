package kinopenapi

import (
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// 这一层把 Go 类型翻译成 JSON Schema。
// kin-openapi 自带 openapi3gen 能做类似的事，但它按纯 Go 结构体走，
// 会把 protobuf 的 well-known types（比如 Timestamp）展开成 {seconds, nanos}，
// 那不是 API 调用方该看到的东西。所以这里保留自己的翻译规则。

var timeType = reflect.TypeFor[time.Time]()

// wellKnown 用类型全名匹配 protobuf well-known types。
// 走字符串而不是导入包，是为了让这个适配包不必直接依赖 protobuf。
var wellKnown = map[string]*openapi3.Schema{
	"google.golang.org/protobuf/types/known/timestamppb.Timestamp": {Format: "date-time"},
	"google.golang.org/protobuf/types/known/durationpb.Duration":   {Format: "duration"},
	"google.golang.org/protobuf/types/known/wrapperspb.Int64Value": {},
}

type schemaResolver struct {
	comps openapi3.Schemas
	names map[reflect.Type]string
	owner map[string]reflect.Type
	stack map[reflect.Type]bool
}

func newSchemaResolver() *schemaResolver {
	return &schemaResolver{
		comps: openapi3.Schemas{},
		names: map[reflect.Type]string{},
		owner: map[string]reflect.Type{},
		stack: map[reflect.Type]bool{},
	}
}

func (r *schemaResolver) components() openapi3.Schemas { return r.comps }

func (r *schemaResolver) Resolve(t reflect.Type) *openapi3.SchemaRef {
	return r.resolve(t, false)
}

func (r *schemaResolver) resolve(t reflect.Type, nullable bool) *openapi3.SchemaRef {
	if t == nil {
		return refOf(openapi3.NewObjectSchema())
	}

	if full := typeFullName(t); full != "" {
		if wkt, ok := wellKnown[full]; ok {
			cp := copySchema(wkt)
			if cp.Type == nil {
				cp.Type = &openapi3.Types{"string"}
			}
			return withNull(refOf(cp), nullable)
		}
	}

	for t.Kind() == reflect.Pointer {
		nullable = true
		t = t.Elem()
	}

	if t == timeType {
		return withNull(scalar("string", "date-time"), nullable)
	}

	switch t.Kind() {
	case reflect.Bool:
		return withNull(scalar("boolean", ""), nullable)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return withNull(scalar("integer", "int32"), nullable)
	case reflect.Int64:
		return withNull(scalar("integer", "int64"), nullable)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return withNull(scalar("integer", "int64"), nullable)

	case reflect.Float32:
		return withNull(scalar("number", "float"), nullable)
	case reflect.Float64:
		return withNull(scalar("number", "double"), nullable)

	case reflect.String:
		return withNull(scalar("string", ""), nullable)

	case reflect.Interface:
		return openapi3.NewSchemaRef("", &openapi3.Schema{})

	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return withNull(scalar("string", "byte"), nullable)
		}
		items := r.resolve(t.Elem(), false)
		s := &openapi3.Schema{Type: &openapi3.Types{"array"}, Items: items}
		return withNull(refOf(s), nullable)

	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return refOf(openapi3.NewObjectSchema())
		}
		s := &openapi3.Schema{
			Type:                 &openapi3.Types{"object"},
			AdditionalProperties: openapi3.AdditionalProperties{Schema: r.resolve(t.Elem(), false)},
		}
		return withNull(refOf(s), nullable)

	case reflect.Struct:
		if t.Name() != "" {
			name := r.name(t)
			// 循环引用：发 $ref 就停，不再展开。
			if r.stack[t] {
				return withNull(openapi3.NewSchemaRef("#/components/schemas/"+name, nil), nullable)
			}
			r.stack[t] = true
			body := r.structSchema(t)
			delete(r.stack, t)
			r.comps[name] = refOf(body)
			return withNull(openapi3.NewSchemaRef("#/components/schemas/"+name, nil), nullable)
		}
		return withNull(refOf(r.structSchema(t)), nullable)
	}

	return refOf(openapi3.NewObjectSchema())
}

func (r *schemaResolver) structSchema(t reflect.Type) *openapi3.Schema {
	s := openapi3.NewObjectSchema()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || skipped(f) {
			continue
		}
		name := jsonName(f)
		fs := r.Resolve(f.Type)
		if d := f.Tag.Get("doc"); d != "" && fs.Value != nil {
			fs.Value.Description = d
		}
		s.Properties[name] = fs
		if hasValidateRule(f, "required") {
			s.Required = append(s.Required, name)
		}
	}
	return s
}

// name 为具名类型分配稳定的组件名，重名时加上包名。
func (r *schemaResolver) name(t reflect.Type) string {
	if n, ok := r.names[t]; ok {
		return n
	}
	base := t.Name()
	if other, taken := r.owner[base]; !taken || other == t {
		r.owner[base] = t
		r.names[t] = base
		return base
	}
	pkg := path.Base(t.PkgPath())
	candidate := pkg + "_" + base
	for i := 2; ; i++ {
		if _, dup := r.owner[candidate]; !dup {
			break
		}
		candidate = pkg + "_" + base + "_" + itoa(i)
	}
	r.owner[candidate] = t
	r.names[t] = candidate
	return candidate
}

// withNull 用 JSON Schema 2020-12 的方式表达可空。
func withNull(ref *openapi3.SchemaRef, nullable bool) *openapi3.SchemaRef {
	if !nullable {
		return ref
	}
	if ref.Ref != "" {
		return refOf(&openapi3.Schema{
			AnyOf: openapi3.SchemaRefs{ref, refOf(&openapi3.Schema{Type: &openapi3.Types{"null"}})},
		})
	}
	s := ref.Value
	if s == nil || s.Type == nil {
		return ref
	}
	cp := copySchema(s)
	if len(*s.Type) == 1 && (*s.Type)[0] != "null" {
		types := openapi3.Types{(*s.Type)[0], "null"}
		cp.Type = &types
	}
	return refOf(cp)
}

func refOf(s *openapi3.Schema) *openapi3.SchemaRef { return openapi3.NewSchemaRef("", s) }

func scalar(typ, format string) *openapi3.SchemaRef {
	ts := openapi3.Types{typ}
	s := &openapi3.Schema{Type: &ts}
	if format != "" {
		s.Format = format
	}
	return refOf(s)
}

func copySchema(s *openapi3.Schema) *openapi3.Schema {
	cp := *s
	if s.Type != nil {
		types := append(openapi3.Types{}, *s.Type...)
		cp.Type = &types
	}
	return &cp
}

func skipped(f reflect.StructField) bool {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return tag == "-"
}

func hasValidateRule(f reflect.StructField, want string) bool {
	for _, rule := range strings.Split(f.Tag.Get("validate"), ",") {
		if strings.TrimSpace(rule) == want {
			return true
		}
	}
	return false
}

func jsonName(f reflect.StructField) string {
	tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if tag != "" && tag != "-" {
		return tag
	}
	return f.Name
}

func typeFullName(t reflect.Type) string {
	if t == nil || t.Name() == "" {
		return ""
	}
	if t.PkgPath() == "" {
		return t.Name()
	}
	return t.PkgPath() + "." + t.Name()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
