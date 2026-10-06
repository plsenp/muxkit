package muxkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/plsenp/muxkit"
)

// ---------- 领域类型 ----------

type GetUserReq struct {
	ID      string `path:"id"`
	Verbose bool   `query:"verbose"`
	Token   string `header:"X-Token"`
}

type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type CreateUserReq struct {
	Name string `json:"name" validate:"required,min=2,max=20"`
	Age  int    `json:"age" validate:"min=0,max=150"`
	Role string `json:"role" validate:"oneof=admin user guest"`
}

// ---------- handler：签名与 grpc 生成的服务方法完全一致 ----------

func getUser(ctx context.Context, req *GetUserReq) (*User, error) {
	return &User{ID: req.ID, Name: "毛毛雨", Age: 18}, nil
}

func createUser(ctx context.Context, req *CreateUserReq) (*User, error) {
	muxkit.Ctx(ctx).SetStatus(http.StatusCreated)
	return &User{Name: req.Name, Age: req.Age, ID: "u-1"}, nil
}

func deleteUser(ctx context.Context, req *GetUserReq) (*User, error) {
	return nil, muxkit.NotFound("用户不存在: " + req.ID)
}

// 核心库零依赖，所以测试里用一个手写校验器，不引第三方。
func stubValidator() muxkit.Validator {
	return muxkit.ValidatorFunc(func(v any) error {
		req, ok := v.(*CreateUserReq)
		if !ok {
			return nil
		}
		if len(req.Name) < 2 {
			return &muxkit.ValidationError{
				Message: "参数校验失败",
				Details: []muxkit.FieldError{{Field: "name", Message: "长度不能小于 2"}},
			}
		}
		return nil
	})
}

func newRouter() *muxkit.Router {
	r := muxkit.New(muxkit.WithValidator(stubValidator()))
	// 泛型方法：Req / Resp 全部从 handler 推断，调用点不写类型参数
	r.GET("/users/{id}", getUser).Summary("获取用户").Tags("user").OperationID("GetUser")
	r.POST("/users", createUser).Summary("创建用户").Tags("user").OperationID("CreateUser")
	r.DELETE("/users/{id}", deleteUser).OperationID("DeleteUser")
	return r
}

// ---------- 测试 ----------

func TestGET_PathQueryHeaderBinding(t *testing.T) {
	r := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/users/u-42?verbose=true", nil)
	req.Header.Set("X-Token", "tok-abc")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200；body=%s", rec.Code, rec.Body.String())
	}
	var got User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got.ID != "u-42" {
		t.Errorf("path 参数没绑上：ID = %q，期望 u-42", got.ID)
	}
}

func TestGET_QueryTypes(t *testing.T) {
	type req2 struct {
		Verbose bool     `query:"verbose"`
		Page    int      `query:"page"`
		Tags    []string `query:"tag"`
	}
	type out struct {
		Verbose bool     `json:"verbose"`
		Page    int      `json:"page"`
		Tags    []string `json:"tags"`
	}
	r := muxkit.New()
	r.GET("/probe", func(ctx context.Context, in *req2) (*out, error) {
		return &out{Verbose: in.Verbose, Page: in.Page, Tags: in.Tags}, nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe?verbose=true&page=3&tag=a&tag=b", nil))

	var got out
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if !got.Verbose || got.Page != 3 || len(got.Tags) != 2 || got.Tags[1] != "b" {
		t.Errorf("query 绑定不完整：%+v", got)
	}
}

func TestPOST_BodyBinding(t *testing.T) {
	r := newRouter()

	body := `{"name":"毛毛雨","age":18,"role":"admin"}`
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("状态码 = %d，期望 201；body=%s", rec.Code, rec.Body.String())
	}
}

func TestPOST_ValidationFailure(t *testing.T) {
	r := newRouter()

	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码 = %d，期望 422；body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "details") {
		t.Errorf("响应缺少 details：%s", rec.Body.String())
	}
	t.Logf("422 响应: %s", rec.Body.String())
}

func TestPOST_StrictBodyRejectsUnknownField(t *testing.T) {
	r := newRouter()
	req := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{"name":"ok","nope":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应触发 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

func TestHTTPError_MapsStatusCode(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/users/ghost", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404；body=%s", rec.Code, rec.Body.String())
	}
}

// 文档生成器只依赖 Routes() 暴露的元数据，核心自己不再产出文档。
func TestRoutes_ExposesMetadata(t *testing.T) {
	r := newRouter()
	routes := r.Routes()
	if len(routes) != 3 {
		t.Fatalf("路由数 = %d，期望 3", len(routes))
	}
	var get *muxkit.RouteMeta
	for _, m := range routes {
		if m.Method == http.MethodGet {
			get = m
		}
	}
	if get == nil {
		t.Fatal("没有 GET 路由")
	}
	if get.Pattern != "/users/{id}" {
		t.Errorf("Pattern = %q", get.Pattern)
	}
	if get.ReqType != reflect.TypeFor[GetUserReq]() {
		t.Errorf("ReqType = %v", get.ReqType)
	}
	if get.RespType != reflect.TypeFor[User]() {
		t.Errorf("RespType = %v", get.RespType)
	}
	if get.OperationID != "GetUser" || len(get.Tags) != 1 {
		t.Errorf("文档元数据丢失：%+v", get)
	}
}

func TestFieldType_NestedPath(t *testing.T) {
	type inner struct {
		ID string `json:"id"`
	}
	type outer struct {
		User inner `json:"user"`
	}
	ft, err := muxkit.FieldType(reflect.TypeFor[outer](), "User.ID")
	if err != nil {
		t.Fatalf("FieldType 失败：%v", err)
	}
	if ft.Kind() != reflect.String {
		t.Errorf("字段类型 = %v，期望 string", ft)
	}
}

// 代码生成的绑定函数走 BindFunc：完全绕开反射和 struct tag
func TestRoute_BindFuncReplacesReflection(t *testing.T) {
	type bindReq struct {
		ID    string
		Token string
	}
	r := muxkit.New()
	r.GET("/x/{id}", func(ctx context.Context, in *bindReq) (*User, error) {
		return &User{ID: in.ID, Name: in.Token}, nil
	}).BindFunc(func(h *http.Request, dst *bindReq) error {
		dst.ID = h.PathValue("id")
		dst.Token = h.Header.Get("X-Token")
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/x/abc", nil)
	req.Header.Set("X-Token", "tok")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "abc") || !strings.Contains(rec.Body.String(), "tok") {
		t.Errorf("BindFunc 没生效：%s", rec.Body.String())
	}
}

// protobuf 路线需要路由级 Renderer：响应必须走 protojson 而不是 encoding/json
func TestRoute_RendererOverride(t *testing.T) {
	r := muxkit.New()
	r.GET("/y", func(ctx context.Context, in *struct{}) (*User, error) {
		muxkit.Ctx(ctx).SetStatus(http.StatusAccepted)
		return &User{Name: "x"}, nil
	}).Renderer(muxkit.RendererFunc(func(w http.ResponseWriter, h *http.Request, v any) error {
		w.Header().Set("Content-Type", "application/json")
		if c := muxkit.Ctx(h.Context()); c != nil {
			if code := c.Status(); code != 0 {
				w.WriteHeader(code)
			}
		}
		_, err := w.Write([]byte(`{"custom":true}`))
		return err
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/y", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("自定义 Renderer 丢了状态码：%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"custom":true`) {
		t.Errorf("自定义 Renderer 没被调用：%s", rec.Body.String())
	}
}

// 注册冲突必须 panic：这是启动阶段的程序员错误，不是运行时错误。
// 库刻意不做兜底——兜底只会把问题藏到线上。
func TestDuplicateRoute_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册同一 pattern 应该 panic")
		}
	}()
	r := muxkit.New()
	h := func(ctx context.Context, in *struct{}) (*User, error) { return nil, nil }
	r.GET("/dup", h)
	r.GET("/dup", h)
}

// 重叠但能分出胜负的不算冲突：字面量比通配符更具体，ServeMux 让它优先。
// 这是 Go 1.22 路由的行为，不是本库加的规则。
func TestOverlappingPatterns_MostSpecificWins(t *testing.T) {
	r := muxkit.New()
	r.GET("/a/{x}", func(ctx context.Context, in *struct{}) (*User, error) {
		return &User{Name: "wildcard"}, nil
	})
	r.GET("/a/b", func(ctx context.Context, in *struct{}) (*User, error) {
		return &User{Name: "literal"}, nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/b", nil))
	if !strings.Contains(rec.Body.String(), "literal") {
		t.Errorf("/a/b 应该命中字面量路由：%s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/zzz", nil))
	if !strings.Contains(rec.Body.String(), "wildcard") {
		t.Errorf("/a/zzz 应该命中通配符路由：%s", rec.Body.String())
	}
}

// 真正会 panic 的是：两个模式重叠，但谁都不比谁更具体
func TestAmbiguousConflict_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("/a/{x} 与 /{y}/b 这种分不出胜负的重叠应该 panic")
		}
	}()
	r := muxkit.New()
	h := func(ctx context.Context, in *struct{}) (*User, error) { return nil, nil }
	r.GET("/a/{x}", h)
	r.GET("/{y}/b", h)
}

// Group 共享同一个 ServeMux，所以跨组冲突同样要在启动时暴露
func TestCrossGroupConflict_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("跨 Group 的冲突应该 panic")
		}
	}()
	r := muxkit.New()
	h := func(ctx context.Context, in *struct{}) (*User, error) { return nil, nil }
	r.Group("/v1").GET("/same", h)
	r.Group("/v1").GET("/same", h)
}

func TestGroup_Prefix(t *testing.T) {
	r := muxkit.New()
	v1 := r.Group("/v1")
	v1.GET("/ping", func(ctx context.Context, in *struct{}) (*User, error) {
		return &User{Name: "pong"}, nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/ping 状态码 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pong") {
		t.Errorf("响应异常：%s", rec.Body.String())
	}
}

// ---------- 中间件 ----------

func namedMW(name string, log *[]string) muxkit.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*log = append(*log, name)
			next.ServeHTTP(w, r)
		})
	}
}

// 路由级中间件只作用于那一条路由
func TestRouteLevelMiddleware_IsScoped(t *testing.T) {
	r := muxkit.New()
	h := func(ctx context.Context, in *struct{}) (*User, error) { return &User{Name: "ok"}, nil }
	gate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Gated", "1")
			next.ServeHTTP(w, r)
		})
	}

	r.GET("/gated", h, gate)
	r.GET("/open", h)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/gated", nil))
	if rec.Header().Get("X-Gated") != "1" {
		t.Error("路由级中间件没生效")
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/open", nil))
	if rec.Header().Get("X-Gated") != "" {
		t.Error("路由级中间件泄漏到别的路由了")
	}
}

// 全局在外层、路由级在内层：recover / access log 才能兜住路由级中间件
func TestMiddlewareOrder_GlobalThenRoute(t *testing.T) {
	var order []string
	r := muxkit.New(muxkit.WithMiddleware(namedMW("global", &order)))
	r.GET("/x", func(ctx context.Context, in *struct{}) (*User, error) {
		order = append(order, "handler")
		return &User{}, nil
	}, namedMW("route1", &order), namedMW("route2", &order))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	want := []string{"global", "route1", "route2", "handler"}
	if len(order) != len(want) {
		t.Fatalf("执行顺序 = %v，期望 %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("执行顺序 = %v，期望 %v", order, want)
		}
	}
}

// 中间件在绑定之前执行，所以鉴权失败时根本不会去解析 body
func TestRouteMiddleware_CanShortCircuitBeforeBinding(t *testing.T) {
	r := muxkit.New()
	r.POST("/secure", func(ctx context.Context, in *CreateUserReq) (*User, error) {
		t.Error("中间件已拦截，handler 不该被调用")
		return nil, nil
	}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/secure", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d，期望 401", rec.Code)
	}
}

// fixedBinder 演示绑定实现可以被整体替换。
type fixedBinder struct{}

func (fixedBinder) Bind(r *http.Request, dst any, meta *muxkit.RouteMeta) error {
	reflect.ValueOf(dst).Elem().FieldByName("Name").SetString("来自自定义 Binder")
	return nil
}

func TestCustomBinder_CanBeReplaced(t *testing.T) {
	r := muxkit.New(muxkit.WithBinder(fixedBinder{}))
	r.GET("/x", func(ctx context.Context, in *CreateUserReq) (*User, error) {
		return &User{Name: in.Name}, nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if !strings.Contains(rec.Body.String(), "来自自定义 Binder") {
		t.Fatalf("自定义 Binder 没生效：%s", rec.Body.String())
	}
}

func TestCustomValidator_CanBeReplaced(t *testing.T) {
	r := muxkit.New(muxkit.WithValidator(muxkit.ValidatorFunc(func(any) error {
		return muxkit.BadRequest("一律拒绝")
	})))
	r.POST("/x", func(ctx context.Context, in *CreateUserReq) (*User, error) {
		return &User{}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"ok"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "一律拒绝") {
		t.Fatalf("自定义 Validator 没生效：%d %s", rec.Code, rec.Body.String())
	}
}

// TestHandlerContextHeaderOnSuccess 是 Set-Cookie 事故的回归测试：
// writeError 一直会应用 HandlerContext 的自定义头，但成功路径此前
// 从不应用 —— 登录接口的 Set-Cookie 在 200 响应上静默消失。
// 头是 handler 在没有 ResponseWriter 时唯一的响应副通道，两条路径必须一致。
func TestHandlerContextHeaderOnSuccess(t *testing.T) {
	r := muxkit.New()
	r.GET("/probe", func(ctx context.Context, req *struct{}) (*struct{}, error) {
		muxkit.Ctx(ctx).Header().Add("Set-Cookie", "k=v; HttpOnly")
		muxkit.Ctx(ctx).SetStatus(201)
		return &struct{}{}, nil
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if got := rec.Header().Get("Set-Cookie"); got != "k=v; HttpOnly" {
		t.Fatalf("成功响应必须带上 handler 设置的头，实际 Set-Cookie=%q", got)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("SetStatus 应当生效，实际 %d", rec.Code)
	}
}
