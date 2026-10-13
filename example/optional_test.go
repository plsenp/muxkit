package main

import (
	"net/http"
	"testing"
)

// ---------- proto3 optional ----------
//
// optional 字段在 Go 里是指针，绑定的关键不是"能赋上值"，
// 而是"没传时保持 nil"——它决定了业务能不能区分"没传"和"传了零值"。
// protojson 渲染响应时会省略零值，所以断言一律看 has_* 标志。

func present(m map[string]any, key string) bool {
	v, ok := m[key]
	return ok && v == true
}

// 一个 optional 参数都不传：全部保持 nil
func TestOptional_Query_AllUnset(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/v1/search", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	for _, k := range []string{"hasKeyword", "hasLimit", "hasIncludeBanned", "hasStatus"} {
		if present(got, k) {
			t.Errorf("%s 不该为 true：%v", k, got)
		}
	}
	t.Logf("全不传：%s", rec.Body.String())
}

// 传的是零值也要算"传了"：空串 / 0 / false / 枚举零值
func TestOptional_Query_ZeroValuesArePresent(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet,
		"/v1/search?keyword=&limit=0&includeBanned=false&status=STATUS_UNSPECIFIED", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	for _, k := range []string{"hasKeyword", "hasLimit", "hasIncludeBanned", "hasStatus"} {
		if !present(got, k) {
			t.Errorf("%s 应为 true，零值不等于没传：%v", k, got)
		}
	}
	t.Logf("传零值：%s", rec.Body.String())
}

func TestOptional_Query_Values(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet,
		"/v1/search?keyword=毛毛雨&limit=5&includeBanned=true&status=STATUS_BANNED&tags=a&tags=b&sort=id", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if got["keyword"] != "毛毛雨" || got["limit"] != float64(5) ||
		got["includeBanned"] != true || got["status"] != "STATUS_BANNED" || got["sort"] != "id" {
		t.Errorf("optional 值绑定不完整：%v", got)
	}
	tags, ok := got["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Errorf("repeated 没绑上：%v", got)
	}
	t.Logf("全传值：%s", rec.Body.String())
}

// optional 枚举：名字和数字都要能绑
func TestOptional_Query_EnumByNameAndNumber(t *testing.T) {
	for _, tc := range []struct{ q, want string }{
		{"status=STATUS_ACTIVE", "STATUS_ACTIVE"},
		{"status=2", "STATUS_BANNED"},
	} {
		rec := do(newTestRouter(), http.MethodGet, "/v1/search?"+tc.q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 状态码 = %d：%s", tc.q, rec.Code, rec.Body.String())
		}
		got := decodeMap(t, rec)
		if !present(got, "hasStatus") || got["status"] != tc.want {
			t.Errorf("%s 绑成 %v，期望 %s", tc.q, got, tc.want)
		}
	}
}

// optional 字段解析失败必须 400，不能静默留 nil
func TestOptional_Query_InvalidValue(t *testing.T) {
	for _, q := range []string{"limit=abc", "includeBanned=maybe", "status=NOT_A_STATUS"} {
		rec := do(newTestRouter(), http.MethodGet, "/v1/search?"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q 应触发 400，实际 %d：%s", q, rec.Code, rec.Body.String())
		}
	}
}

// optional 的 path 参数 + optional 的 query 参数
func TestOptional_PathAndQuery(t *testing.T) {
	rec := do(newTestRouter(), http.MethodGet, "/v1/profiles/p-1?withStats=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if !present(got, "hasId") || got["id"] != "p-1" {
		t.Errorf("optional path 没绑上：%v", got)
	}
	if got["withStats"] != true {
		t.Errorf("optional query 没绑上：%v", got)
	}
	t.Logf("path+query：%s", rec.Body.String())
}

// body 里的 optional：只有出现在 JSON 里的字段才有值
func TestOptional_Body_OnlyPresentFields(t *testing.T) {
	rec := do(newTestRouter(), http.MethodPatch, "/v1/profiles/p-9", `{"nickname":"岩"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if !present(got, "hasNickname") || got["nickname"] != "岩" {
		t.Errorf("body 里的 optional 没绑上：%v", got)
	}
	if present(got, "hasAge") {
		t.Errorf("body 里没出现的 optional 不该有值：%v", got)
	}
	t.Logf("body 部分字段：%s", rec.Body.String())
}

// body 里显式写 0 也算传了
func TestOptional_Body_ZeroIsPresent(t *testing.T) {
	rec := do(newTestRouter(), http.MethodPatch, "/v1/profiles/p-9", `{"age":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeMap(t, rec)
	if !present(got, "hasAge") {
		t.Errorf("显式传 0 应算传了：%v", got)
	}
	if present(got, "hasNickname") {
		t.Errorf("nickname 不该有值：%v", got)
	}
	t.Logf("body 零值：%s", rec.Body.String())
}
