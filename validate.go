package muxkit

// Validator 在绑定之后、调用 handler 之前对 Req 做校验。
// 默认实现是 NopValidator——库不替你决定校验规则。
type Validator interface {
	Validate(any) error
}

// ValidatorFunc 让普通函数直接充当 Validator。
type ValidatorFunc func(any) error

func (f ValidatorFunc) Validate(v any) error { return f(v) }

type nopValidator struct{}

func (nopValidator) Validate(any) error { return nil }

// NopValidator 是默认校验器：什么都不做。
var NopValidator Validator = nopValidator{}
