// Package muxkit 在 net/http.ServeMux（Go 1.22 增强路由）之上提供类型安全的
// handler 注册、自动参数绑定与校验，以及 OpenAPI 文档生成。
//
// 核心约定：handler 签名与 grpc 生成的服务方法一致——
//
//	func(ctx context.Context, req *Req) (*Resp, error)
//
// 这样同一个实现既可以作为 gRPC service 注册，也可以挂到 HTTP 路由上。
//
// 泛型方法（Go 1.27）让注册入口写成 r.GET(pattern, h) 而不是
// web.GET[Req, Resp](r, pattern, h)：类型由 handler 推断，调用点不需要写类型参数。
// 注意语言限制：泛型方法不能实现接口，所以 Router 的注册方法无法被抽象成接口。
package muxkit

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
)

// HandlerFunc 是库唯一支持的 handler 形状，与 grpc 生成的方法签名一致。
type HandlerFunc[Req, Resp any] func(context.Context, *Req) (*Resp, error)

// Middleware 是标准的 net/http 中间件形状。
type Middleware func(http.Handler) http.Handler

// Router 包装 http.ServeMux，并在注册时记录类型元数据。
type Router struct {
	mux    *http.ServeMux
	prefix string
	root   *Router

	routes []*RouteMeta
	mws    []Middleware

	binder     Binder
	validator  Validator
	renderer   Renderer
	errMapper  ErrorMapper
	strictBody bool
}

// Option 配置 Router。
type Option func(*Router)

// WithBinder 替换全局绑定实现。
func WithBinder(b Binder) Option { return func(r *Router) { r.binder = b } }

// WithValidator 替换全局校验实现。
func WithValidator(v Validator) Option { return func(r *Router) { r.validator = v } }

// WithRenderer 替换响应渲染实现。
func WithRenderer(r2 Renderer) Option { return func(r *Router) { r.renderer = r2 } }

// WithErrorMapper 替换错误到状态码的映射。
func WithErrorMapper(f ErrorMapper) Option { return func(r *Router) { r.errMapper = f } }

// WithMiddleware 追加中间件。必须在注册路由之前调用——
// 中间件是在注册那一刻织入的，之后追加不会影响已注册的路由。
func WithMiddleware(mws ...Middleware) Option {
	return func(r *Router) { r.mws = append(r.mws, mws...) }
}

// WithStrictBody 控制请求体是否拒绝未知字段。默认开启。
func WithStrictBody(strict bool) Option { return func(r *Router) { r.strictBody = strict } }

// New 创建 Router。
func New(opts ...Option) *Router {
	rt := &Router{
		mux:        http.NewServeMux(),
		strictBody: true,
	}
	for _, o := range opts {
		o(rt)
	}
	if rt.binder == nil {
		rt.binder = newDefaultBinder(rt.strictBody)
	}
	if rt.validator == nil {
		rt.validator = NopValidator
	}
	if rt.renderer == nil {
		rt.renderer = JSONRenderer{}
	}
	if rt.errMapper == nil {
		rt.errMapper = DefaultErrorMapper
	}
	rt.root = rt
	return rt
}

// Group 派生一个带路径前缀的子路由，与主路由共享同一个 ServeMux。
func (rt *Router) Group(prefix string) *Router {
	return &Router{
		mux:        rt.mux,
		prefix:     rt.prefix + prefix,
		root:       rt.rootRouter(),
		mws:        append([]Middleware{}, rt.mws...),
		binder:     rt.binder,
		validator:  rt.validator,
		renderer:   rt.renderer,
		errMapper:  rt.errMapper,
		strictBody: rt.strictBody,
	}
}

// Use 追加中间件，只在后续注册的路由上生效。
func (rt *Router) Use(mws ...Middleware) {
	rt.mws = append(rt.mws, mws...)
}

func (rt *Router) rootRouter() *Router {
	if rt.root != nil {
		return rt.root
	}
	return rt
}

// ServeHTTP 让 Router 本身就是一个 http.Handler。
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}

// Mux 暴露底层 ServeMux，便于挂载非 muxkit 的 handler。
func (rt *Router) Mux() *http.ServeMux { return rt.mux }

// Routes 返回全部已注册路由的元数据，主要给文档生成器使用。
func (rt *Router) Routes() []*RouteMeta { return rt.rootRouter().routes }

// Handle 是全部注册方法的实现。它是泛型方法：Req / Resp 由 h 推断。
//
// 路由冲突会直接 panic，这是有意的：注册冲突是程序员错误，不是运行时错误，
// 应该在服务启动阶段就炸掉，而不是兜底成 500 把问题藏到线上。
// 底层的 http.ServeMux 也是这个设计，panic 消息里会带上两处 pattern 的注册位置。
//
// 什么样的重叠算冲突由 ServeMux 判定：能分出更具体的那个不算冲突
// （"/a/{x}" 和 "/a/b" 可以共存，字面量优先）；重叠但分不出胜负才 panic。
//
// mws 是只作用于这一条路由的中间件。鉴权、限流这类东西基本都不该全站统一，
// 所以注册时直接带上，比事后靠路径前缀去匹配要可靠。
// 执行顺序是：Router 级（Use/WithMiddleware）→ 路由级 mws → handler。
// 全局在外层，是为了让 recover、access log 这类能兜住路由级中间件自己出的问题。
func (rt *Router) Handle[Req, Resp any](method, pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	root := rt.rootRouter()
	full := rt.prefix + pattern
	key := method + " " + full

	meta := &RouteMeta{
		Method:   method,
		Pattern:  full,
		ReqType:  reflect.TypeFor[Req](),
		RespType: reflect.TypeFor[Resp](),
	}
	root.routes = append(root.routes, meta)

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt.serve(w, r, h, meta)
	})
	// 先包路由级（内层），再包全局级（外层）
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}
	for i := len(rt.mws) - 1; i >= 0; i-- {
		handler = rt.mws[i](handler)
	}
	rt.mux.Handle(key, handler)

	return &Route[Req, Resp]{meta: meta, router: rt}
}

// GET 注册 GET 路由（ServeMux 会自动让它同时匹配 HEAD）。
func (rt *Router) GET[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodGet, pattern, h, mws...)
}

// POST 注册 POST 路由。
func (rt *Router) POST[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodPost, pattern, h, mws...)
}

// PUT 注册 PUT 路由。
func (rt *Router) PUT[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodPut, pattern, h, mws...)
}

// PATCH 注册 PATCH 路由。
func (rt *Router) PATCH[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodPatch, pattern, h, mws...)
}

// DELETE 注册 DELETE 路由。
func (rt *Router) DELETE[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodDelete, pattern, h, mws...)
}

// HEAD 注册 HEAD 路由。
func (rt *Router) HEAD[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodHead, pattern, h, mws...)
}

// OPTIONS 注册 OPTIONS 路由。
func (rt *Router) OPTIONS[Req, Resp any](pattern string, h HandlerFunc[Req, Resp], mws ...Middleware) *Route[Req, Resp] {
	return rt.Handle[Req, Resp](http.MethodOptions, pattern, h, mws...)
}

func (rt *Router) serve[Req, Resp any](w http.ResponseWriter, r *http.Request, h HandlerFunc[Req, Resp], meta *RouteMeta) {
	c := &HandlerContext{r: r}
	ctx := context.WithValue(r.Context(), handlerCtxKey{}, c)
	r = r.WithContext(ctx)

	binder := rt.binder
	if meta.binder != nil {
		binder = meta.binder
	}
	validator := rt.validator
	if meta.validator != nil {
		validator = meta.validator
	}

	var req Req
	if meta.bind != nil {
		// 代码生成的绑定：不走反射，也不看 struct tag
		if err := meta.bind.bind(r, &req); err != nil {
			rt.writeError(w, r, err)
			return
		}
	} else if err := binder.Bind(r, &req, meta); err != nil {
		rt.writeError(w, r, err)
		return
	}
	if err := validator.Validate(&req); err != nil {
		rt.writeError(w, r, err)
		return
	}

	renderer := rt.renderer
	if meta.renderer != nil {
		renderer = meta.renderer
	}

	resp, err := h(ctx, &req)
	if err != nil {
		rt.writeError(w, r, err)
		return
	}
	if err := renderer.Render(w, r, resp); err != nil {
		rt.writeError(w, r, err)
	}
}

func (rt *Router) writeError(w http.ResponseWriter, r *http.Request, err error) {
	code, body := rt.errMapper(r, err)
	if c := Ctx(r.Context()); c != nil {
		for k, vs := range c.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
