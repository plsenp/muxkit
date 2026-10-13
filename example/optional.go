package main

import (
	"context"

	"example.com/demo/pb"
)

// optionalService 演示 proto3 optional（字段带 presence）的绑定。
//
// 签名与 protoc-gen-go-grpc 生成的 OptionalServiceServer 一致，
// 所以同一个实现既能挂 grpc.Server 也能挂 muxkit.Router。
type optionalService struct {
	pb.UnimplementedOptionalServiceServer
}

// SearchUsers 把 presence 显式回显出来。
// protojson 会省略零值字段，测试只能靠 has_* 判断"没传"和"传了零值"。
func (s *optionalService) SearchUsers(ctx context.Context, req *pb.SearchUsersRequest) (*pb.SearchUsersResponse, error) {
	resp := &pb.SearchUsersResponse{
		Tags: req.GetTags(),
		Sort: req.GetSort(),
	}
	if v := req.Keyword; v != nil {
		resp.HasKeyword, resp.Keyword = true, *v
	}
	if v := req.Limit; v != nil {
		resp.HasLimit, resp.Limit = true, *v
	}
	if v := req.IncludeBanned; v != nil {
		resp.HasIncludeBanned, resp.IncludeBanned = true, *v
	}
	if v := req.Status; v != nil {
		resp.HasStatus, resp.Status = true, *v
	}
	return resp, nil
}

// GetProfile 的 id 是 optional 的 path 参数，with_stats 是 optional 的 query 参数。
func (s *optionalService) GetProfile(ctx context.Context, req *pb.GetProfileRequest) (*pb.Profile, error) {
	resp := &pb.Profile{}
	if v := req.Id; v != nil {
		resp.HasId, resp.Id = true, *v
	}
	if v := req.WithStats; v != nil {
		resp.WithStats = *v
	}
	return resp, nil
}

// PatchProfile 的 body 由 protojson 反序列化，只有出现在 JSON 里的字段才有值。
func (s *optionalService) PatchProfile(ctx context.Context, req *pb.PatchProfileRequest) (*pb.Profile, error) {
	resp := &pb.Profile{Id: req.Id, HasId: req.Id != ""}
	if v := req.Nickname; v != nil {
		resp.HasNickname, resp.Nickname = true, v
	}
	if v := req.Age; v != nil {
		resp.HasAge, resp.Age = true, v
	}
	return resp, nil
}
