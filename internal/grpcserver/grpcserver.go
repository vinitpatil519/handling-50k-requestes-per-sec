// Package grpcserver exposes the standard gRPC health service. titanload's
// gRPC mode drives grpc.health.v1.Health/Check, which needs no generated code
// on either side yet exercises the full HTTP/2 + protobuf stack.
package grpcserver

import (
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

// New returns a gRPC server and its health registry.
func New() (*grpc.Server, *health.Server) {
	srv := grpc.NewServer(
		grpc.MaxConcurrentStreams(1024),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionAge:      5 * time.Minute, // forces client rebalancing across pods
			MaxConnectionAgeGrace: 30 * time.Second,
			Time:                  30 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(srv, hs)
	reflection.Register(srv)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hs.SetServingStatus("titanedge.api", healthpb.HealthCheckResponse_SERVING)
	return srv, hs
}
