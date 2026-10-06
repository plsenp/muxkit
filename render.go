package muxkit

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
)

// Renderer 把 handler 的返回值写进响应。
type Renderer interface {
	Render(w http.ResponseWriter, r *http.Request, v any) error
}

// RendererFunc 让普通函数直接充当 Renderer。
// 代码生成的渲染函数就靠它接进来。
type RendererFunc func(w http.ResponseWriter, r *http.Request, v any) error

func (f RendererFunc) Render(w http.ResponseWriter, r *http.Request, v any) error {
	return f(w, r, v)
}

// JSONRenderer 是默认实现：写 application/json，并尊重 HandlerContext 里设的状态码。
type JSONRenderer struct{}

// Render 实现 Renderer。nil 响应（含 typed nil 指针）直接回 204。
//
// handler 通过 HandlerContext.Header() 设置的头在这里生效 ——
// 此前只有 writeError 应用它们，成功路径上的 Set-Cookie / Location
// 会静默消失，而签名限制让 handler 没有任何别的途径设头。
// Content-Type 由渲染器自己定（handler 设了也会被覆盖）：
// 响应体的媒体类型是渲染器的职责，不该由业务 handler 决定。
func (JSONRenderer) Render(w http.ResponseWriter, r *http.Request, v any) error {
	// 头的应用必须在一切 WriteHeader 之前 —— 包括 204 的早退分支：
	// 「没有响应体」不等于「没有响应头」，登出类接口恰恰只有头有意义。
	applyCtxHeader(w, r)
	if isNil(v) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	writeStatus(w, r)
	_, err = w.Write(b)
	return err
}

// applyCtxHeader 把 HandlerContext 上的自定义头拷到真实响应上。
func applyCtxHeader(w http.ResponseWriter, r *http.Request) {
	if c := Ctx(r.Context()); c != nil {
		for k, vs := range c.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
}

func writeStatus(w http.ResponseWriter, r *http.Request) {
	if c := Ctx(r.Context()); c != nil && c.status != 0 {
		w.WriteHeader(c.status)
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// HandlerContext 让 handler 在没有 http.ResponseWriter 的情况下
// 也能设置状态码和响应头——这是兼容 grpc 签名的必要代价。
type HandlerContext struct {
	r      *http.Request
	status int
	header http.Header
}

type handlerCtxKey struct{}

// Ctx 取出当前请求的 HandlerContext。
// 在 muxkit 之外使用（比如单测里直接调 handler）会返回 nil，所有方法都是安全的空操作。
func Ctx(ctx context.Context) *HandlerContext {
	c, _ := ctx.Value(handlerCtxKey{}).(*HandlerContext)
	return c
}

// Request 返回原始请求。
func (c *HandlerContext) Request() *http.Request {
	if c == nil {
		return nil
	}
	return c.r
}

// SetStatus 覆盖响应状态码，比如新建资源时设 201。
func (c *HandlerContext) SetStatus(code int) {
	if c == nil {
		return
	}
	c.status = code
}

// Status 返回 handler 设置的状态码，0 表示没设过。
// 自定义 Renderer 需要读它，否则 SetStatus 会静默失效。
func (c *HandlerContext) Status() int {
	if c == nil {
		return 0
	}
	return c.status
}

// Header 返回会被合并进响应头的集合。
func (c *HandlerContext) Header() http.Header {
	if c == nil {
		return nil
	}
	if c.header == nil {
		c.header = make(http.Header)
	}
	return c.header
}
