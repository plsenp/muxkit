// Package kinopenapi 用 github.com/getkin/kin-openapi 从 muxkit 的路由元数据
// 生成 OpenAPI 3.1 文档。
//
// 放在独立 module 里，核心库因此保持零第三方依赖：
// 不喜欢这个依赖的项目可以换别的生成器，或者直接用 r.Routes() 自己拼。
//
// 用法：
//
//	doc, err := kinopenapi.Build(r, &openapi3.Info{Title: "用户服务", Version: "v1"})
//	kinopenapi.Serve(r, "/openapi.json", &openapi3.Info{Title: "用户服务", Version: "v1"})
package kinopenapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"

	"github.com/plsenp/muxkit"

	"github.com/getkin/kin-openapi/openapi3"
)

type config struct {
	servers  openapi3.Servers
	tags     openapi3.Tags
	validate bool
	filter   func(*muxkit.RouteMeta) bool
}

// Option 配置文档生成。
type Option func(*config)

// WithServers 设置 servers。
func WithServers(servers ...*openapi3.Server) Option {
	return func(c *config) { c.servers = append(c.servers, servers...) }
}

// WithTags 预声明标签。
func WithTags(tags ...*openapi3.Tag) Option {
	return func(c *config) { c.tags = append(c.tags, tags...) }
}

// WithRouteFilter 只把符合条件的路由写进文档。
//
// 这个适配器面向传统 API 开发（struct tag 绑定）。protobuf 路线的参数映射写在
// google.api.http 里、由代码生成消化，文档该交给 gnostic，因此通常要过滤掉：
//
//	kinopenapi.WithRouteFilter(func(m *muxkit.RouteMeta) bool {
//	    return !slices.Contains(m.Tags, "UserService")
//	})
func WithRouteFilter(f func(*muxkit.RouteMeta) bool) Option {
	return func(c *config) { c.filter = f }
}

// WithValidation 在返回文档前跑一遍 openapi3 的校验。
// 建议开着——这是用成熟库换来的主要好处之一：文档写错了能在启动阶段就发现。
func WithValidation(enable bool) Option {
	return func(c *config) { c.validate = enable }
}

// Build 从 Router 已注册的路由生成 OpenAPI 3.1 文档。
func Build(r *muxkit.Router, info *openapi3.Info, opts ...Option) (*openapi3.T, error) {
	var c config
	for _, o := range opts {
		o(&c)
	}

	res := newSchemaResolver()
	doc := &openapi3.T{
		OpenAPI: "3.1.0",
		Info:    info,
		Paths:   openapi3.NewPaths(),
		Servers: c.servers,
		Tags:    c.tags,
	}

	for _, m := range r.Routes() {
		if c.filter != nil && !c.filter(m) {
			continue
		}
		doc.AddOperation(toOpenAPIPath(m.Pattern), m.Method, buildOperation(res, m))
	}

	if len(res.components()) > 0 {
		doc.Components = &openapi3.Components{Schemas: res.components()}
	}

	if c.validate {
		// SchemaRef 只有在被 Loader 链接过之后才带 Value；直接对程序化构建的
		// 文档调 Validate，所有 $ref 都会被判成 unresolved。所以先序列化再加载。
		data, err := doc.MarshalJSON()
		if err != nil {
			return nil, err
		}
		loaded, err := openapi3.NewLoader().LoadFromData(data)
		if err != nil {
			return nil, err
		}
		if err := loaded.Validate(context.Background()); err != nil {
			return nil, err
		}
		return loaded, nil
	}
	return doc, nil
}

// Serve 把文档挂到一个 GET 路由上，随代码更新，不需要额外构建步骤。
func Serve(r *muxkit.Router, pattern string, info *openapi3.Info, opts ...Option) {
	r.Mux().HandleFunc("GET "+pattern, func(w http.ResponseWriter, req *http.Request) {
		doc, err := Build(r, info, opts...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}

func buildOperation(res *schemaResolver, m *muxkit.RouteMeta) *openapi3.Operation {
	op := openapi3.NewOperation()
	op.OperationID = m.OperationID
	op.Summary = m.Summary
	op.Description = m.Description
	op.Tags = m.Tags
	op.Deprecated = m.Deprecated

	if len(m.Params) > 0 {
		for _, p := range m.Params {
			op.AddParameter(buildParam(res, m, p))
		}
	} else {
		for _, p := range paramsFromTags(m.ReqType, res) {
			op.AddParameter(p)
		}
	}

	if body := requestBody(res, m); body != nil {
		op.RequestBody = &openapi3.RequestBodyRef{Value: body}
	}

	success := openapi3.NewResponse().WithDescription("成功")
	if m.RespType != nil {
		success.WithContent(openapi3.NewContentWithSchemaRef(
			res.Resolve(m.RespType), []string{"application/json"}))
	}
	errResp := openapi3.NewResponse().WithDescription("错误").WithContent(
		openapi3.NewContentWithSchemaRef(
			res.Resolve(reflect.TypeFor[muxkit.ErrorBody]()), []string{"application/json"}))

	op.Responses = openapi3.NewResponses(
		openapi3.WithStatus(http.StatusOK, &openapi3.ResponseRef{Value: success}),
		openapi3.WithName("default", errResp),
	)
	return op
}

func buildParam(res *schemaResolver, m *muxkit.RouteMeta, p muxkit.ParamSpec) *openapi3.Parameter {
	var param *openapi3.Parameter
	switch p.In {
	case muxkit.InPath:
		param = openapi3.NewPathParameter(p.Name)
	case muxkit.InQuery:
		param = openapi3.NewQueryParameter(p.Name)
	case muxkit.InHeader:
		param = openapi3.NewHeaderParameter(p.Name)
	default:
		return openapi3.NewQueryParameter(p.Name)
	}
	param.Required = p.In == muxkit.InPath
	if m.ReqType != nil {
		if ft, err := muxkit.FieldType(m.ReqType, p.Field); err == nil {
			param.Schema = res.Resolve(ft)
		}
	}
	return param
}

func requestBody(res *schemaResolver, m *muxkit.RouteMeta) *openapi3.RequestBody {
	if !m.AllowsBody() || m.ReqType == nil {
		return nil
	}
	var schema *openapi3.SchemaRef
	if m.Body != nil && m.Body.Field != "*" {
		if ft, err := muxkit.FieldType(m.ReqType, m.Body.Field); err == nil {
			schema = res.Resolve(ft)
		}
	}
	if schema == nil {
		schema = res.Resolve(m.ReqType)
	}
	return openapi3.NewRequestBody().
		WithRequired(true).
		WithContent(openapi3.NewContentWithSchemaRef(schema, []string{"application/json"}))
}

// paramsFromTags 在没有任何显式 ParamSpec 时，从 struct tag 推导参数。
func paramsFromTags(t reflect.Type, res *schemaResolver) []*openapi3.Parameter {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []*openapi3.Parameter
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		var in muxkit.ParamIn
		switch {
		case f.Tag.Get("path") != "":
			in = muxkit.InPath
		case f.Tag.Get("query") != "":
			in = muxkit.InQuery
		case f.Tag.Get("header") != "":
			in = muxkit.InHeader
		default:
			continue
		}
		name := tagName(f, in)
		if name == "" {
			continue
		}
		var param *openapi3.Parameter
		switch in {
		case muxkit.InPath:
			param = openapi3.NewPathParameter(name)
		case muxkit.InQuery:
			param = openapi3.NewQueryParameter(name)
		default:
			param = openapi3.NewHeaderParameter(name)
		}
		param.Required = in == muxkit.InPath || hasValidateRule(f, "required")
		param.Schema = res.Resolve(f.Type)
		out = append(out, param)
	}
	return out
}

func tagName(f reflect.StructField, in muxkit.ParamIn) string {
	v := f.Tag.Get(string(in))
	if v == "" || v == "-" {
		return ""
	}
	name, _, _ := strings.Cut(v, ",")
	if name == "" {
		name = jsonName(f)
	}
	return name
}

// toOpenAPIPath 把 ServeMux 的 pattern 转成 OpenAPI 路径模板。
// {id} 两边一致；要处理的是 {path...} 通配和 {$} 结束锚点。
func toOpenAPIPath(pattern string) string {
	segs := strings.Split(pattern, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch {
		case s == "{$}":
			continue
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			name := s[1 : len(s)-1]
			if i := strings.Index(name, "="); i >= 0 {
				name = name[:i]
			}
			name = strings.TrimSuffix(name, "...")
			out = append(out, "{"+name+"}")
		default:
			out = append(out, s)
		}
	}
	p := strings.Join(out, "/")
	if p == "" {
		p = "/"
	}
	return p
}
