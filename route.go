package muxkit

import (
	"net/http"
	"reflect"
)

// ParamIn 表示参数在 HTTP 请求中的来源位置。
type ParamIn string

const (
	InPath   ParamIn = "path"
	InQuery  ParamIn = "query"
	InHeader ParamIn = "header"
)

// ParamSpec 是「请求中的某个名字」到「Req 结构体某个字段」的显式映射。
// protoc 插件会生成这些映射；手写 Go 结构体时通常由 struct tag 推导，不需要它。
type ParamSpec struct {
	In    ParamIn
	Name  string // 请求侧的名字：path 模板名 / query key / header 名
	Field string // 目标字段路径，支持 "user.id" 这种点号嵌套
}

// BodySpec 描述请求体如何落到 Req 上。
// Field == "*" 表示整个 Req 由请求体反序列化而来；否则填充指定字段。
type BodySpec struct {
	Field string
}

// RouteMeta 是单条路由的全部元数据。它既驱动运行期的绑定，也驱动 OpenAPI 生成，
// 两者共用同一份事实，避免文档与实现漂移。
type RouteMeta struct {
	Method  string
	Pattern string

	Params []ParamSpec
	Body   *BodySpec

	ReqType  reflect.Type
	RespType reflect.Type

	// 路由级覆盖；nil 表示沿用 Router 的全局实现
	binder    Binder
	validator Validator
	renderer  Renderer

	// 代码生成的绑定函数。优先级高于 Binder——它零反射，也不需要扫 tag
	bind routeBinder

	// 文档字段
	OperationID string
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool
}

// AllowsBody 返回该路由是否按约定去读请求体。
// 显式设置了 BodySpec 时以显式配置为准——google.api.http 允许在任意方法上带 body。
func (m *RouteMeta) AllowsBody() bool {
	if m.Body != nil {
		return true
	}
	switch m.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// routeBinder 让非泛型的 RouteMeta 能持有类型安全的绑定函数。
// Go 不允许 RouteMeta 自己泛型化（否则它没法塞进统一的 routes 切片），
// 所以用这个接口在泛型边界上抹掉一层类型。
type routeBinder interface {
	bind(*http.Request, any) error
}

type typedBinder[Req any] struct {
	fn func(*http.Request, *Req) error
}

func (b typedBinder[Req]) bind(r *http.Request, dst any) error {
	return b.fn(r, dst.(*Req))
}

// Route 是注册方法的返回值，用于链式补充元数据。
// 它是泛型类型，所以链式调用全程保留 Req/Resp 的类型信息。
type Route[Req, Resp any] struct {
	meta   *RouteMeta
	router *Router
}

// Meta 暴露内部元数据，供自定义 Binder / OpenAPI 生成器读取。
func (r *Route[Req, Resp]) Meta() *RouteMeta { return r.meta }

func (r *Route[Req, Resp]) Path(name, field string) *Route[Req, Resp] {
	r.meta.Params = append(r.meta.Params, ParamSpec{In: InPath, Name: name, Field: field})
	return r
}

func (r *Route[Req, Resp]) Query(name, field string) *Route[Req, Resp] {
	r.meta.Params = append(r.meta.Params, ParamSpec{In: InQuery, Name: name, Field: field})
	return r
}

func (r *Route[Req, Resp]) Header(name, field string) *Route[Req, Resp] {
	r.meta.Params = append(r.meta.Params, ParamSpec{In: InHeader, Name: name, Field: field})
	return r
}

// Body 声明请求体的映射目标。"*" 表示整体映射。
func (r *Route[Req, Resp]) Body(field string) *Route[Req, Resp] {
	r.meta.Body = &BodySpec{Field: field}
	return r
}

func (r *Route[Req, Resp]) OperationID(id string) *Route[Req, Resp] {
	r.meta.OperationID = id
	return r
}

func (r *Route[Req, Resp]) Summary(s string) *Route[Req, Resp] {
	r.meta.Summary = s
	return r
}

func (r *Route[Req, Resp]) Description(s string) *Route[Req, Resp] {
	r.meta.Description = s
	return r
}

func (r *Route[Req, Resp]) Tags(tags ...string) *Route[Req, Resp] {
	r.meta.Tags = append(r.meta.Tags, tags...)
	return r
}

func (r *Route[Req, Resp]) MarkDeprecated() *Route[Req, Resp] {
	r.meta.Deprecated = true
	return r
}

// Binder 为这一条路由单独替换绑定实现。
func (r *Route[Req, Resp]) Binder(b Binder) *Route[Req, Resp] {
	r.meta.binder = b
	return r
}

// BindFunc 用编译期确定的函数替换本路由的绑定逻辑。
// protoc 插件生成的绑定函数走这条路：零反射、零 tag 扫描，
// path / query / body 的映射全部由 google.api.http 在生成期决定。
func (r *Route[Req, Resp]) BindFunc(fn func(*http.Request, *Req) error) *Route[Req, Resp] {
	r.meta.bind = typedBinder[Req]{fn: fn}
	return r
}

// Validator 为这一条路由单独替换校验实现。
func (r *Route[Req, Resp]) Validator(v Validator) *Route[Req, Resp] {
	r.meta.validator = v
	return r
}

// Renderer 为这一条路由单独替换响应渲染。
// protobuf 路线需要它：响应必须走 protojson，否则 int64 会被渲染成数字、
// enum 也变成数字，和 protobuf 生态的 JSON 约定不一致。
func (r *Route[Req, Resp]) Renderer(rd Renderer) *Route[Req, Resp] {
	r.meta.renderer = rd
	return r
}
