package kinopenapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/plsenp/muxkit"
	"github.com/plsenp/muxkit/adapter/kinopenapi"

	"github.com/getkin/kin-openapi/openapi3"
)

type GetReq struct {
	ID      string `path:"id"`
	Verbose bool   `query:"verbose"`
	Token   string `header:"X-Token"`
}

type User struct {
	ID   string   `json:"id"`
	Name string   `json:"name" validate:"required"`
	Tags []string `json:"tags"`
	Age  int32    `json:"age"`
}

type CreateReq struct {
	Name string `json:"name" validate:"required,min=2"`
}

func buildRouter() *muxkit.Router {
	r := muxkit.New()
	r.GET("/users/{id}", func(ctx context.Context, in *GetReq) (*User, error) {
		return &User{ID: in.ID}, nil
	}).OperationID("GetUser").Tags("user").Summary("获取用户")
	r.POST("/users", func(ctx context.Context, in *CreateReq) (*User, error) {
		return &User{Name: in.Name}, nil
	}).OperationID("CreateUser").Body("*")
	return r
}

func TestBuild_PassesOpenAPIValidation(t *testing.T) {
	doc, err := kinopenapi.Build(buildRouter(), &openapi3.Info{Title: "t", Version: "v1"},
		kinopenapi.WithValidation(true))
	if err != nil {
		t.Fatalf("文档没通过校验：%v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("版本 = %q", doc.OpenAPI)
	}
}

func TestBuild_PathsAndParameters(t *testing.T) {
	doc, err := kinopenapi.Build(buildRouter(), &openapi3.Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	item := doc.Paths.Find("/users/{id}")
	if item == nil || item.Get == nil {
		t.Fatalf("缺少 GET /users/{id}：%v", doc.Paths.Keys())
	}
	op := item.Get
	if op.OperationID != "GetUser" || op.Summary != "获取用户" {
		t.Errorf("元数据丢失：%+v", op)
	}

	byName := map[string]*openapi3.Parameter{}
	for _, p := range op.Parameters {
		if p.Value == nil {
			continue
		}
		byName[p.Value.Name] = p.Value
	}
	for _, want := range []string{"id", "verbose", "X-Token"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("缺少参数 %q，实际 %v", want, byName)
		}
	}
	if !byName["id"].Required {
		t.Error("path 参数必须标记 required")
	}
	if byName["verbose"].Required {
		t.Error("query 参数不该是 required")
	}
	if byName["X-Token"].In != "header" {
		t.Errorf("X-Token 的 in = %q", byName["X-Token"].In)
	}
}

func TestBuild_RequestBodyAndComponents(t *testing.T) {
	doc, err := kinopenapi.Build(buildRouter(), &openapi3.Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	post := doc.Paths.Find("/users").Post
	if post == nil || post.RequestBody == nil {
		t.Fatal("POST 缺少 requestBody")
	}
	if doc.Components == nil {
		t.Fatal("components 为空")
	}
	for _, want := range []string{"User", "CreateReq"} {
		if _, ok := doc.Components.Schemas[want]; !ok {
			t.Errorf("缺少 schema %q：%v", want, doc.Components.Schemas)
		}
	}
	user := doc.Components.Schemas["User"]
	if user.Value == nil || len(user.Value.Required) == 0 || user.Value.Required[0] != "name" {
		t.Errorf("User.required 没从 validate tag 推导出来：%+v", user.Value)
	}
}

// 循环引用必须收敛成 $ref，不能无限展开
func TestBuild_CircularReference(t *testing.T) {
	type Node struct {
		Name     string  `json:"name"`
		Children []*Node `json:"children,omitempty"`
	}
	r := muxkit.New()
	r.GET("/tree", func(ctx context.Context, in *struct{}) (*Node, error) { return nil, nil })

	doc, err := kinopenapi.Build(r, &openapi3.Info{Title: "t", Version: "v1"},
		kinopenapi.WithValidation(true))
	if err != nil {
		t.Fatalf("循环引用导致文档非法：%v", err)
	}
	if _, ok := doc.Components.Schemas["Node"]; !ok {
		t.Errorf("Node 没有进入 components：%v", doc.Components.Schemas)
	}
}

// 通配符和结束锚点要转成 OpenAPI 的路径模板
func TestToOpenAPIPath(t *testing.T) {
	type empty struct{}
	h := func(ctx context.Context, in *empty) (*empty, error) { return nil, nil }

	r := muxkit.New()
	r.GET("/files/{path...}", h)
	r.GET("/exact/{$}", h)

	doc, err := kinopenapi.Build(r, &openapi3.Info{Title: "t", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/files/{path}", "/exact"} {
		if doc.Paths.Find(want) == nil {
			t.Errorf("缺少路径 %q，实际 %v", want, doc.Paths.Keys())
		}
	}
}

func TestServe(t *testing.T) {
	r := buildRouter()
	kinopenapi.Serve(r, "/openapi.json", &openapi3.Info{Title: "用户服务", Version: "v1"})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var doc openapi3.T
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("不是合法 OpenAPI JSON：%v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("版本 = %q", doc.OpenAPI)
	}
}
