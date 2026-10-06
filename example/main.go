// 演示：protobuf 定义的 service，自动生成路由，同一个实现既能跑 gRPC 也能跑 HTTP。
package main

import (
	"context"
	"log"
	"net/http"

	"example.com/demo/pb"
	"muxkit"
	"muxkit/adapter/kinopenapi"
	"muxkit/adapter/validator"

	"github.com/getkin/kin-openapi/openapi3"
)

type userService struct {
	pb.UnimplementedUserServiceServer
}

// 签名与 protoc-gen-go-grpc 生成的服务方法完全一致，
// 所以下面这些函数既可以直接注册到 grpc.Server，也可以注册到 muxkit.Router。
func (s *userService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.User, error) {
	return &pb.User{Id: req.GetId(), Name: "毛毛雨", Age: 18}, nil
}

func (s *userService) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	// 回显绑定结果，方便端到端验证 enum / repeated / 字符串是否正确落位
	return &pb.ListUsersResponse{
		Users:  []*pb.User{{Id: "u-1", Name: "岩"}, {Id: "u-2", Name: "毛毛雨"}},
		Total:  2,
		Status: req.GetStatus(),
		TagIds: req.GetTagIds(),
		Page:   req.GetPage(),
	}, nil
}

func (s *userService) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.User, error) {
	muxkit.Ctx(ctx).SetStatus(http.StatusCreated)
	return &pb.User{Id: "u-new", Name: req.GetName(), Age: req.GetAge(), Tags: req.GetTags()}, nil
}

func (s *userService) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.User, error) {
	u := req.GetUser()
	u.Id = req.GetId()
	return u, nil
}

func (s *userService) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.Empty, error) {
	log.Printf("删除用户 %s", req.GetId())
	return &pb.Empty{}, nil
}

func (s *userService) GetUserByEmail(ctx context.Context, req *pb.GetUserByEmailRequest) (*pb.User, error) {
	return &pb.User{Id: "u-mail", Name: req.GetEmail()}, nil
}

// ---------- 路线二：传统 API 开发 ----------
//
// 没有 proto，参数靠 struct tag 声明：path / query / header 由核心的默认
// Binder（基于 go-playground/form）处理，校验用 v10，文档用 kin-openapi。

type legacyListReq struct {
	ID   string `path:"id" validate:"required"`
	Page int    `query:"page" validate:"omitempty,min=1,max=100"`
	Size int    `query:"size" validate:"omitempty,min=1,max=50"`
	KW   string `query:"kw" validate:"omitempty,min=2"`
}

type legacyListResp struct {
	ID    string   `json:"id"`
	Page  int      `json:"page"`
	Size  int      `json:"size"`
	Items []string `json:"items"`
}

func legacyList(ctx context.Context, req *legacyListReq) (*legacyListResp, error) {
	size := req.Size
	if size == 0 {
		size = 20
	}
	return &legacyListResp{ID: req.ID, Page: req.Page, Size: size, Items: []string{"a", "b"}}, nil
}

// withRequestID 是路由级中间件，只挂在 legacy 那条路由上。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "demo-"+r.PathValue("id"))
		next.ServeHTTP(w, r)
	})
}

func isLegacyRoute(m *muxkit.RouteMeta) bool {
	for _, t := range m.Tags {
		if t == "legacy" {
			return true
		}
	}
	return false
}

func main() {
	r := muxkit.New(muxkit.WithValidator(validator.New()))

	// 路线一：protobuf —— 路由、绑定都由 protoc 插件生成，
	// 校验交给 protovalidate，文档交给 gnostic（都不走核心）。
	pb.RegisterUserServiceRoutes(r, &userService{})

	// 路线二：传统 API —— struct tag 绑定 + v10 校验 + kin-openapi 文档。
	// 这条路的 req 完全由手写结构体描述，所以 kin-openapi 能推导出参数。
	// 末尾的可变长参数只作用于这一条路由——鉴权、限流基本都不该全站统一。
	r.GET("/legacy/users/{id}", legacyList, withRequestID).
		OperationID("LegacyListUsers").Tags("legacy").Summary("传统方式：struct tag 绑定")

	// 文档只覆盖传统路线：protobuf 那边的参数映射在 google.api.http 里，
	// 已经由代码生成消化掉了，这里的反射推导看不到，硬生成只会缺参数。
	kinopenapi.Serve(r, "/openapi.json", &openapi3.Info{
		Title:   "用户服务",
		Version: "v1.0.0",
	},
		kinopenapi.WithServers(&openapi3.Server{URL: "http://localhost:8080"}),
		kinopenapi.WithRouteFilter(isLegacyRoute),
		kinopenapi.WithValidation(true),
	)

	log.Printf("监听 :8080，OpenAPI 文档在 /openapi.json")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal(err)
	}
}
