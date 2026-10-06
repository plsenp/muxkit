package muxkit

import (
	"encoding"
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()

// fieldByPath 按 "a.b.c" 形式定位字段。沿途遇到 nil 指针会自动分配，
// 这样 protobuf 的嵌套 message / optional 字段可以被直接写入。
func fieldByPath(v reflect.Value, path string) (reflect.Value, error) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		for cur.Kind() == reflect.Pointer {
			if cur.IsNil() {
				if !cur.CanSet() {
					return reflect.Value{}, fmt.Errorf("muxkit: 无法为路径 %q 分配 nil 指针", path)
				}
				cur.Set(reflect.New(cur.Type().Elem()))
			}
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("muxkit: 路径 %q 中 %q 不是结构体", path, seg)
		}
		f := cur.FieldByName(seg)
		if !f.IsValid() {
			f = fieldByJSONName(cur, seg)
		}
		if !f.IsValid() {
			return reflect.Value{}, fmt.Errorf("muxkit: 找不到字段 %q（路径 %q）", seg, path)
		}
		if !f.CanSet() {
			return reflect.Value{}, fmt.Errorf("muxkit: 字段 %q 不可写（路径 %q）", seg, path)
		}
		cur = f
	}
	return cur, nil
}

// fieldByJSONName 允许用 json tag 的名字寻址，方便 codegen 直接吐 wire name。
func fieldByJSONName(st reflect.Value, name string) reflect.Value {
	t := st.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == name {
			return st.Field(i)
		}
	}
	return reflect.Value{}
}

// FieldType 按 "a.b.c" 形式的字段路径取类型。
// 导出它，是为了让 OpenAPI 适配包能给参数生成 schema，而不必重复实现反射寻址。
func FieldType(t reflect.Type, path string) (reflect.Type, error) {
	return typeByPath(t, path)
}

// typeByPath 是 fieldByPath 的类型层版本，供 OpenAPI 生成参数 schema 时使用。
func typeByPath(t reflect.Type, path string) (reflect.Type, error) {
	cur := t
	for _, seg := range strings.Split(path, ".") {
		for cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return nil, fmt.Errorf("muxkit: 路径 %q 中 %q 不是结构体", path, seg)
		}
		f, ok := cur.FieldByName(seg)
		if !ok {
			if jf := structFieldByJSONName(cur, seg); jf != nil {
				f = *jf
				ok = true
			}
		}
		if !ok {
			return nil, fmt.Errorf("muxkit: 找不到字段 %q（路径 %q）", seg, path)
		}
		cur = f.Type
	}
	return cur, nil
}

func structFieldByJSONName(t reflect.Type, name string) *reflect.StructField {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == name {
			sf := f
			return &sf
		}
	}
	return nil
}

// setFieldValue 把若干字符串写入目标值。
// 传多个值时用于 query 的重复参数（?tag=a&tag=b），单值时直接取第一个。
func setFieldValue(dst reflect.Value, vals []string) error {
	if len(vals) == 0 {
		return nil
	}
	if !dst.CanSet() {
		return fmt.Errorf("muxkit: 目标字段不可写")
	}

	for dst.Kind() == reflect.Pointer {
		if dst.IsNil() {
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		dst = dst.Elem()
	}

	if dst.CanAddr() && dst.Type().Implements(textUnmarshalerType) {
		return unmarshalText(dst, vals[0])
	}
	if dst.CanAddr() {
		if u, ok := dst.Addr().Interface().(encoding.TextUnmarshaler); ok {
			if err := u.UnmarshalText([]byte(vals[0])); err != nil {
				return fmt.Errorf("muxkit: 解析 %q 失败: %w", vals[0], err)
			}
			return nil
		}
	}

	switch dst.Kind() {
	case reflect.String:
		dst.SetString(vals[0])

	case reflect.Bool:
		b, err := strconv.ParseBool(vals[0])
		if err != nil {
			return fmt.Errorf("muxkit: %q 不是合法布尔值", vals[0])
		}
		dst.SetBool(b)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(vals[0], 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("muxkit: %q 不是合法整数", vals[0])
		}
		dst.SetInt(n)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(vals[0], 10, dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("muxkit: %q 不是合法无符号整数", vals[0])
		}
		dst.SetUint(n)

	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(vals[0], dst.Type().Bits())
		if err != nil {
			return fmt.Errorf("muxkit: %q 不是合法浮点数", vals[0])
		}
		dst.SetFloat(f)

	case reflect.Slice:
		if dst.Type().Elem().Kind() == reflect.Uint8 {
			raw, err := base64.StdEncoding.DecodeString(vals[0])
			if err != nil {
				return fmt.Errorf("muxkit: %q 不是合法 base64", vals[0])
			}
			dst.SetBytes(raw)
			return nil
		}
		sl := reflect.MakeSlice(dst.Type(), len(vals), len(vals))
		for i, v := range vals {
			if err := setFieldValue(sl.Index(i), []string{v}); err != nil {
				return err
			}
		}
		dst.Set(sl)

	default:
		return fmt.Errorf("muxkit: 不支持的字段类型 %s", dst.Type())
	}
	return nil
}

func unmarshalText(dst reflect.Value, raw string) error {
	u, ok := dst.Addr().Interface().(encoding.TextUnmarshaler)
	if !ok {
		return fmt.Errorf("muxkit: %s 未实现 encoding.TextUnmarshaler", dst.Type())
	}
	if err := u.UnmarshalText([]byte(raw)); err != nil {
		return fmt.Errorf("muxkit: 解析 %q 失败: %w", raw, err)
	}
	return nil
}
