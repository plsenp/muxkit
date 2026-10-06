# muxkit

在 `net/http.ServeMux`（Go 1.22 增强路由）之上提供类型安全的 handler 注册、自动参数绑定与校验，以及 OpenAPI 文档生成。

## 核心约定

handler 签名与 gRPC 生成的服务方法一致：

```go
func(ctx context.Context, req *Req) (*Resp, error)
```

这样同一个实现既可以作为 gRPC service 注册，也可以挂到 HTTP 路由上。

## 特性

- 类型安全的路由注册：`r.GET(pattern, handler)` / `r.POST(...)`,类型由 handler 推断
- 自动请求参数绑定（query / path / body）
- 请求校验（Validator）
- 统一响应渲染与错误映射（Renderer / ErrorMapper）
- OpenAPI 文档生成
- 标准 net/http 中间件形状

## 要求

- Go 1.27+

## License

[MIT](LICENSE)