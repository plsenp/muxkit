package muxkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/form/v4"
)

// Binder 负责把 *http.Request 填充进 Req。
// 接口方法不能带类型参数，而且绑定本质是运行时反射，所以这里是 any 而不是泛型——
// 这个位置本来就不需要编译期类型信息。
type Binder interface {
	Bind(r *http.Request, dst any, meta *RouteMeta) error
}

// DefaultMaxBodyBytes 是请求体的默认上限，防止被大 body 打爆内存。
const DefaultMaxBodyBytes = 1 << 20

type defaultBinder struct {
	pathDec   *form.Decoder
	queryDec  *form.Decoder
	headerDec *form.Decoder
	formDec   *form.Decoder
	maxBody   int64
	strict    bool
}

func newDefaultBinder(strict bool) *defaultBinder {
	tagged := func(tag string) *form.Decoder {
		d := form.NewDecoder()
		d.SetTagName(tag)
		return d
	}
	return &defaultBinder{
		pathDec:   tagged("path"),
		queryDec:  tagged("query"),
		headerDec: tagged("header"),
		formDec:   tagged("form"),
		maxBody:   DefaultMaxBodyBytes,
		strict:    strict,
	}
}

func (b *defaultBinder) Bind(r *http.Request, dst any, meta *RouteMeta) error {
	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("muxkit: Binder 需要一个非 nil 指针")
	}
	if rv.Elem().Kind() != reflect.Struct {
		return b.decodeBody(r, dst)
	}
	st := rv.Elem()

	// 显式模式：有 Params/Body 时完全按声明走，不看 struct tag。
	// 三种绑定模式（显式 / tag / 代码生成）互不混用，行为才可预测。
	if len(meta.Params) > 0 || meta.Body != nil {
		return b.bindExplicit(r, st, meta)
	}
	return b.bindByTag(r, st, meta)
}

// bindByTag 是传统 API 开发的默认路径：
// url.Values 类的来源（query / header / form）交给 form 库处理，
// path 参数因为 ServeMux 只暴露了 PathValue(key)，只能自己按 tag 收一遍再喂进去。
func (b *defaultBinder) bindByTag(r *http.Request, st reflect.Value, meta *RouteMeta) error {
	dst := st.Addr().Interface()

	// 1. body 先解
	if meta.AllowsBody() {
		if err := b.decodeBody(r, dst); err != nil {
			return err
		}
	}

	// 2. path 覆盖
	if names := pathNamesOf(st.Type()); len(names) > 0 {
		vals := make(url.Values, len(names))
		for _, n := range names {
			if v := r.PathValue(n); v != "" {
				vals.Set(n, v)
			}
		}
		if len(vals) > 0 {
			if err := b.pathDec.Decode(dst, vals); err != nil {
				return &BindError{Err: err}
			}
		}
	}

	// 3. query / header 覆盖
	if q := r.URL.Query(); len(q) > 0 {
		if err := b.queryDec.Decode(dst, q); err != nil {
			return &BindError{Err: err}
		}
	}
	if len(r.Header) > 0 {
		if err := b.headerDec.Decode(dst, url.Values(r.Header)); err != nil {
			return &BindError{Err: err}
		}
	}
	return nil
}

// bindExplicit 按路由上声明的 Params / Body 绑定，用于结构体不在自己控制下的场景。
// protobuf 路线不走这里——它用代码生成的绑定函数。
func (b *defaultBinder) bindExplicit(r *http.Request, st reflect.Value, meta *RouteMeta) error {
	// body 先解，参数后覆盖。顺序反了，body 的零值会把 path/query 参数抹掉。
	if meta.Body != nil {
		target := st
		if meta.Body.Field != "*" {
			f, err := fieldByPath(st, meta.Body.Field)
			if err != nil {
				return &BindError{Err: err}
			}
			target = f
		}
		if target.CanAddr() {
			if err := b.decodeBody(r, target.Addr().Interface()); err != nil {
				return err
			}
		}
	}

	for _, p := range meta.Params {
		vals := paramValues(r, p)
		if len(vals) == 0 {
			continue
		}
		f, err := fieldByPath(st, p.Field)
		if err != nil {
			return &BindError{Field: p.Field, Err: err}
		}
		if err := setFieldValue(f, vals); err != nil {
			return &BindError{Field: p.Field, Err: err}
		}
	}
	return nil
}

func paramValues(r *http.Request, p ParamSpec) []string {
	switch p.In {
	case InPath:
		if v := r.PathValue(p.Name); v != "" {
			return []string{v}
		}
	case InQuery:
		return r.URL.Query()[p.Name]
	case InHeader:
		return r.Header.Values(p.Name)
	}
	return nil
}

func (b *defaultBinder) decodeBody(r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	mt = strings.TrimSpace(mt)

	switch mt {
	case "application/x-www-form-urlencoded":
		if err := r.ParseForm(); err != nil {
			return &BindError{Err: err}
		}
		if err := b.formDec.Decode(dst, r.PostForm); err != nil {
			return &BindError{Err: err}
		}
		return nil

	case "multipart/form-data":
		if err := r.ParseMultipartForm(b.maxBody); err != nil {
			return &BindError{Err: err}
		}
		if err := b.formDec.Decode(dst, r.MultipartForm.Value); err != nil {
			return &BindError{Err: err}
		}
		return nil

	case "application/json", "":
		var src io.Reader = r.Body
		if b.maxBody > 0 {
			src = io.LimitReader(r.Body, b.maxBody)
		}
		dec := json.NewDecoder(src)
		if b.strict {
			dec.DisallowUnknownFields()
		}
		if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
			return &BindError{Err: err}
		}
		return nil

	default:
		return &BindError{Err: fmt.Errorf("muxkit: 不支持的 Content-Type %q", mt)}
	}
}

// pathNameCache 缓存 struct 的 path tag 扫描结果。
// 反射本身不慢，慢的是每个请求都重新遍历一遍字段。
var pathNameCache sync.Map // reflect.Type -> []string

func pathNamesOf(t reflect.Type) []string {
	if cached, ok := pathNameCache.Load(t); ok {
		return cached.([]string)
	}
	var names []string
	// use type.Fields instead t.NumField
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		v := f.Tag.Get("path")
		if v == "" || v == "-" {
			continue
		}
		name, _, _ := strings.Cut(v, ",")
		if name == "" {
			name = f.Name
		}
		names = append(names, name)
	}
	// 空结果也存，免得没有 path tag 的结构体每次重扫
	pathNameCache.Store(t, names)
	return names
}
