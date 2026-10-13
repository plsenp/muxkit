// Package validator 用 github.com/go-playground/validator/v10 实现 muxkit.Validator。
//
// 放在独立 module 里，是为了让核心库保持零第三方依赖：
// 不想吃这个依赖的项目可以自己实现 muxkit.Validator，或者直接用 NopValidator。
//
// 用法：
//
//	r := muxkit.New(muxkit.WithValidator(validator.New()))
//
// 注意 tag 语义与自研校验器不同：v10 的 min/max 对字符串是长度、对数字是数值，
// 所以原先的 minlen=2 现在写 min=2。
package validator

import (
	"errors"
	"reflect"
	"strings"

	"github.com/plsenp/muxkit"

	val "github.com/go-playground/validator/v10"
)

// Validator 把 v10 的校验结果翻译成 muxkit.ValidationError，
// 这样 Router 的默认 ErrorMapper 才能给出 422 和 details。
type Validator struct {
	v        *val.Validate
	messages map[string]string
}

// Option 配置校验器。
type Option func(*Validator)

// WithTagNameFunc 自定义错误里的字段名。
func WithTagNameFunc(f func(reflect.StructField) string) Option {
	return func(v *Validator) { v.v.RegisterTagNameFunc(f) }
}

// WithMessage 覆盖某条规则的默认错误文案，比如 WithMessage("required", "不能为空")。
func WithMessage(tag, msg string) Option {
	return func(v *Validator) {
		if v.messages == nil {
			v.messages = map[string]string{}
		}
		v.messages[tag] = msg
	}
}

// WithValidate 直接接管底层的 *validator.Validate，用于注册自定义规则。
func WithValidate(configure func(*val.Validate)) Option {
	return func(v *Validator) { configure(v.v) }
}

// New 创建校验器。
//
// 默认按这个顺序取错误字段名：json tag → 绑定 tag（path/query/header/form）→ Go 字段名。
// 第二步很关键：传统开发里查询参数结构体常常只写 query tag 没有 json tag，
// 少了它，客户端收到的错误字段会是 Page 这种 Go 字段名，毫无意义。
func New(opts ...Option) *Validator {
	x := &Validator{v: val.New()}
	x.v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		for _, key := range []string{"json", "path", "query", "header", "form"} {
			name := strings.SplitN(fld.Tag.Get(key), ",", 2)[0]
			if name != "" && name != "-" {
				return name
			}
		}
		return fld.Name
	})
	for _, o := range opts {
		o(x)
	}
	return x
}

// Validate 实现 muxkit.Validator。
func (x *Validator) Validate(v any) error {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}

	err := x.v.Struct(v)
	if err == nil {
		return nil
	}
	var verrs val.ValidationErrors
	if !errors.As(err, &verrs) {
		// v10 对非结构体入参会返回别的错误，也可能直接 panic；这里只兜住前者
		return &muxkit.ValidationError{Message: err.Error()}
	}
	details := make([]muxkit.FieldError, 0, len(verrs))
	for _, fe := range verrs {
		details = append(details, muxkit.FieldError{
			Field:   fe.Field(),
			Message: x.describe(fe),
		})
	}
	return &muxkit.ValidationError{Message: "参数校验失败", Details: details}
}

func (x *Validator) describe(fe val.FieldError) string {
	if msg, ok := x.messages[fe.Tag()]; ok {
		return msg
	}
	switch fe.Tag() {
	case "required":
		return "字段必填"
	case "min", "max":
		return "取值不在允许范围（" + fe.Tag() + "=" + fe.Param() + "）"
	case "len":
		return "长度必须等于 " + fe.Param()
	case "oneof":
		return "只能是 " + strings.ReplaceAll(fe.Param(), " ", " / ")
	case "email":
		return "不是合法的邮箱地址"
	case "url":
		return "不是合法的 URL"
	case "uuid":
		return "不是合法的 UUID"
	case "gt":
		return "必须大于 " + fe.Param()
	case "gte":
		return "必须大于等于 " + fe.Param()
	case "lt":
		return "必须小于 " + fe.Param()
	case "lte":
		return "必须小于等于 " + fe.Param()
	default:
		return "不满足规则 " + fe.Tag()
	}
}
