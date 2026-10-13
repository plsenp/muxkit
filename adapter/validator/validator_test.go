package validator_test

import (
	"errors"
	"testing"

	"github.com/plsenp/muxkit"
	"github.com/plsenp/muxkit/adapter/validator"
)

type Req struct {
	Name  string `json:"name" validate:"required,min=2,max=20"`
	Age   int    `json:"age" validate:"min=0,max=150"`
	Role  string `json:"role" validate:"oneof=admin user guest"`
	Email string `json:"email" validate:"omitempty,email"`
}

func TestValid(t *testing.T) {
	v := validator.New()
	err := v.Validate(&Req{Name: "毛毛雨", Age: 18, Role: "admin"})
	if err != nil {
		t.Fatalf("合法请求被判失败：%v", err)
	}
}

func TestInvalid_CollectsAllFields(t *testing.T) {
	v := validator.New()
	err := v.Validate(&Req{Name: "x", Age: 999, Role: "root", Email: "not-an-email"})
	if err == nil {
		t.Fatal("非法请求却通过了")
	}

	var ve *muxkit.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("错误类型不是 *muxkit.ValidationError：%T", err)
	}
	if len(ve.Details) < 4 {
		t.Errorf("失败项收集不全，期望至少 4 条，实际 %d：%+v", len(ve.Details), ve.Details)
	}

	// 字段名必须是 json 名，不能是 Go 字段名
	names := map[string]bool{}
	for _, d := range ve.Details {
		names[d.Field] = true
	}
	for _, want := range []string{"name", "age", "role", "email"} {
		if !names[want] {
			t.Errorf("缺少字段 %q；实际 %v", want, names)
		}
	}
	t.Logf("details: %+v", ve.Details)
}

func TestInvalid_StatusMapping(t *testing.T) {
	v := validator.New()
	err := v.Validate(&Req{Name: "x"})

	code, body := muxkit.DefaultErrorMapper(nil, err)
	if code != 422 {
		t.Errorf("状态码 = %d，期望 422", code)
	}
	eb, ok := body.(muxkit.ErrorBody)
	if !ok {
		t.Fatalf("响应体类型 = %T", body)
	}
	if len(eb.Details) == 0 {
		t.Errorf("details 没带到响应里：%+v", eb)
	}
	t.Logf("响应体：%+v", eb)
}

func TestOmitempty_SkipsWhenZero(t *testing.T) {
	v := validator.New()
	if err := v.Validate(&Req{Name: "毛毛雨", Role: "user"}); err != nil {
		t.Errorf("omitempty 字段为空时不该报错：%v", err)
	}
}

func TestNilAndNonStruct(t *testing.T) {
	v := validator.New()
	if err := v.Validate(nil); err != nil {
		t.Errorf("nil 应直接通过：%v", err)
	}
	var p *Req
	if err := v.Validate(p); err != nil {
		t.Errorf("nil 指针应直接通过：%v", err)
	}
}

// 只有绑定 tag、没有 json tag 时，字段名应该退回绑定名而不是 Go 字段名
type onlyTagReq struct {
	Page int `query:"page" validate:"min=1,max=10"`
}

func TestFieldNameFallsBackToBindingTag(t *testing.T) {
	err := validator.New().Validate(&onlyTagReq{Page: 99})
	var ve *muxkit.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("类型不对：%T", err)
	}
	if len(ve.Details) != 1 || ve.Details[0].Field != "page" {
		t.Fatalf("字段名应为 page，实际 %+v", ve.Details)
	}
}

func TestWithMessage_Overrides(t *testing.T) {
	v := validator.New(validator.WithMessage("required", "这个字段必须填"))
	err := v.Validate(&Req{Role: "user"})
	var ve *muxkit.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("类型不对：%T", err)
	}
	for _, d := range ve.Details {
		if d.Field == "name" && d.Message != "这个字段必须填" {
			t.Errorf("自定义文案没生效：%+v", d)
		}
	}
}
