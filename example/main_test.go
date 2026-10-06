package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/demo/pb"
	"muxkit"
	"muxkit/adapter/kinopenapi"
	"muxkit/adapter/validator"

	"github.com/getkin/kin-openapi/openapi3"
)

// newTestRouter 同时挂上两条开发路线的路由：
// protobuf 路线（代码生成）和传统路线（struct tag）。
func newTestRouter() *muxkit.Router {
	r := muxkit.New(muxkit.WithValidator(validator.New()))
	pb.RegisterUserServiceRoutes(r, &userService{})
	r.GET("/legacy/users/{id}", legacyList).
		OperationID("LegacyListUsers").Tags("legacy").Summary("传统方式：struct tag 绑定")
	kinopenapi.Serve(r, "/openapi.json", &openapi3.Info{Title: "用户服务", Version: "v1"},
		kinopenapi.WithRouteFilter(isLegacyRoute))
	return r
}

func do(r *muxkit.Router, method, target, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// 响应由 protojson 渲染，enums 是字符串，所以断言用 map 而不是 pb 结构体
func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（%s）", err, rec.Body.String())
	}
	return m
}

// ---------- 路线一：protobuf + 代码生成 ----------

func TestProtobuf_GetUser_PathAndQuery(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/v1/users/u-42?verbose=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if got["id"] != "u-42" {
		t.Errorf("path 参数没绑上：%v", got)
	}
	t.Logf("GET 响应：%s", rec.Body.String())
}

func TestProtobuf_CreateUser_Body(t *testing.T) {
	rec := do(newTestRouter(), http.MethodPost, "/v1/users", `{"name":"毛毛雨","age":18,"tags":["a","b"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if got["name"] != "毛毛雨" {
		t.Errorf("body 绑定不完整：%v", got)
	}
	t.Logf("POST 响应：%s", rec.Body.String())
}

// body 只映射到 user 字段，id 走路径——最容易写反的组合
func TestProtobuf_UpdateUser_BodyFieldPlusPath(t *testing.T) {
	rec := do(newTestRouter(), http.MethodPatch, "/v1/users/u-9", `{"name":"新名字","age":20}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if got["id"] != "u-9" {
		t.Errorf("路径参数被 body 覆盖了：%v", got)
	}
	if got["name"] != "新名字" {
		t.Errorf("body 没映射到 user 字段：%v", got)
	}
	t.Logf("PATCH 响应：%s", rec.Body.String())
}

func TestProtobuf_DeleteUser(t *testing.T) {
	rec := do(newTestRouter(), http.MethodDelete, "/v1/users/u-42", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
}

func TestProtobuf_AdditionalBinding(t *testing.T) {
	r := newTestRouter()
	for _, p := range []string{"/v1/users/email/a@b.com", "/v1/internal/users/a@b.com"} {
		rec := do(r, http.MethodGet, p, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 = %d", p, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "a@b.com") {
			t.Errorf("%s 的 email 没绑上：%s", p, rec.Body.String())
		}
	}
}

// 代码生成的 query 绑定：enum 支持名字、repeated 展开
func TestProtobuf_Query_EnumAndRepeated(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/v1/users?status=STATUS_ACTIVE&tagIds=1&tagIds=2&page=3", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if got["status"] != "STATUS_ACTIVE" {
		t.Errorf("enum 没从名字绑上：%v", got)
	}
	ids, ok := got["tagIds"].([]any)
	if !ok || len(ids) != 2 {
		t.Errorf("repeated 没绑上：%v", got)
	}
	t.Logf("query 绑定响应：%s", rec.Body.String())
}

// enum 也接受数字
func TestProtobuf_Query_EnumByNumber(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/v1/users?status=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if got := decodeMap(t, rec); got["status"] != "STATUS_BANNED" {
		t.Errorf("enum 数字没绑上：%v", got)
	}
}

// 非法值必须报 400，而不是静默留零值
func TestProtobuf_Query_InvalidValue(t *testing.T) {
	for _, q := range []string{"status=NOT_A_STATUS", "tagIds=abc", "page=notanumber"} {
		rec := do(newTestRouter(), http.MethodGet, "/v1/users?"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q 应触发 400，实际 %d：%s", q, rec.Code, rec.Body.String())
		}
	}
}

// ---------- 路线二：传统 API（struct tag + v10 + kin-openapi）----------

func TestLegacy_PathAndQueryBinding(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/legacy/users/u-7?page=2&size=10&kw=hello", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	var got legacyListResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if got.ID != "u-7" || got.Page != 2 || got.Size != 10 {
		t.Errorf("tag 绑定不完整：%+v", got)
	}
	t.Logf("传统方式响应：%s", rec.Body.String())
}

func TestLegacy_ValidationWithV10(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/legacy/users/u-7?page=999", "")
	if rec.Code != 422 {
		t.Fatalf("page 越界应触发 422，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "details") {
		t.Errorf("响应缺少 details：%s", rec.Body.String())
	}
	t.Logf("422 响应：%s", rec.Body.String())
}

// omitempty 的语义：字段没传就不校验，传了才校验
func TestLegacy_OmitemptySkipsAbsent(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/legacy/users/u-7", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("page/size 缺省时不该报错，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// 文档只覆盖传统路线：protobuf 那边的参数映射在 google.api.http 里，
// 已由代码生成消化，这里的反射推导看不到，硬生成只会产出缺参数的坏文档。
func TestOpenAPI_LegacyOnly(t *testing.T) {
	r := newTestRouter()
	rec := do(r, http.MethodGet, "/openapi.json", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var doc openapi3.T
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("文档不是合法 JSON：%v", err)
	}
	keys := doc.Paths.Keys()
	if len(keys) != 1 || keys[0] != "/legacy/users/{id}" {
		t.Errorf("文档应只包含 legacy 路径，实际 %v", keys)
	}

	op := doc.Paths.Find("/legacy/users/{id}").Get
	names := map[string]string{}
	for _, p := range op.Parameters {
		if p.Value != nil {
			names[p.Value.Name] = p.Value.In
		}
	}
	if names["id"] != "path" || names["page"] != "query" {
		t.Errorf("参数推导不完整：%v", names)
	}

	// 用 kin-openapi 自己校验一遍
	built, err := kinopenapi.Build(r, &openapi3.Info{Title: "用户服务", Version: "v1"},
		kinopenapi.WithRouteFilter(isLegacyRoute),
		kinopenapi.WithValidation(true))
	if err != nil {
		t.Fatalf("文档没通过 openapi3 校验：%v", err)
	}
	if built.Paths.Len() != 1 {
		t.Errorf("过滤失效，paths = %v", built.Paths.Keys())
	}
	t.Logf("paths=%v schemas=%v", built.Paths.Keys(), len(built.Components.Schemas))
}
