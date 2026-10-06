package muxkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// HTTPError 让业务错误自带状态码。业务侧只要实现它，Router 就不需要额外的映射配置。
type HTTPError interface {
	error
	StatusCode() int
}

// StatusError 是一个开箱即用的 HTTPError 实现。
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string   { return e.Msg }
func (e *StatusError) StatusCode() int { return e.Code }

// NewStatusError 构造带状态码的错误。
func NewStatusError(code int, msg string) *StatusError {
	return &StatusError{Code: code, Msg: msg}
}

// BadRequest / NotFound 等是常用状态码的便捷构造。
func BadRequest(msg string) *StatusError   { return NewStatusError(http.StatusBadRequest, msg) }
func Unauthorized(msg string) *StatusError { return NewStatusError(http.StatusUnauthorized, msg) }
func Forbidden(msg string) *StatusError    { return NewStatusError(http.StatusForbidden, msg) }
func NotFound(msg string) *StatusError     { return NewStatusError(http.StatusNotFound, msg) }
func Conflict(msg string) *StatusError     { return NewStatusError(http.StatusConflict, msg) }

// BindError 表示请求数据无法绑定到 Req，默认映射为 400。
type BindError struct {
	Field string
	Err   error
}

func (e *BindError) Error() string {
	if e.Field == "" {
		return "bind: " + e.Err.Error()
	}
	return "bind " + e.Field + ": " + e.Err.Error()
}
func (e *BindError) Unwrap() error { return e.Err }

// FieldError 描述单个字段的校验失败原因。
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError 表示 Req 通过了绑定但没通过校验，默认映射为 422。
// 一次可以同时携带多个字段的失败原因，客户端不必反复试错。
type ValidationError struct {
	Field   string
	Message string
	Details []FieldError
}

func (e *ValidationError) Error() string {
	if len(e.Details) > 0 {
		var b strings.Builder
		b.WriteString("validate: ")
		for i, d := range e.Details {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(d.Field)
			b.WriteString(": ")
			b.WriteString(d.Message)
		}
		return b.String()
	}
	if e.Field == "" {
		return "validate: " + e.Message
	}
	return "validate " + e.Field + ": " + e.Message
}

// ErrorBody 是默认错误响应的 JSON 结构。
type ErrorBody struct {
	Code    string       `json:"code,omitempty"`
	Message string       `json:"message"`
	Details []FieldError `json:"details,omitempty"`
}

// ErrorMapper 把任意 error 翻译成状态码和响应体。
// 默认实现见 DefaultErrorMapper。
type ErrorMapper func(r *http.Request, err error) (int, any)

// StatusCodeOf 是一个小工具，便于自定义 ErrorMapper 复用默认规则。
func StatusCodeOf(err error) int {
	if he, ok := errors.AsType[HTTPError](err); ok {
		return he.StatusCode()
	}

	if _, ok := errors.AsType[*BindError](err); ok {
		return http.StatusBadRequest
	}

	if _, ok := errors.AsType[*ValidationError](err); ok {
		return http.StatusUnprocessableEntity
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, context.Canceled) {
		return 499 // client closed request
	}
	return http.StatusInternalServerError
}

// DefaultErrorMapper 是保守的：5xx 不回传内部错误细节，避免信息泄露。
func DefaultErrorMapper(r *http.Request, err error) (int, any) {
	if he, ok := errors.AsType[HTTPError](err); ok {
		return he.StatusCode(), ErrorBody{Message: he.Error()}
	}

	if be, ok := errors.AsType[*BindError](err); ok {
		return http.StatusBadRequest, ErrorBody{Code: "invalid_argument", Message: be.Error()}
	}

	if ve, ok := errors.AsType[*ValidationError](err); ok {
		return http.StatusUnprocessableEntity, ErrorBody{
			Code:    "invalid_argument",
			Message: ve.Message,
			Details: ve.Details,
		}
	}

	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	case errors.Is(err, context.Canceled):
		code = 499
	}
	if code >= 500 {
		return code, ErrorBody{Code: "internal", Message: http.StatusText(code)}
	}
	return code, ErrorBody{Message: err.Error()}
}
