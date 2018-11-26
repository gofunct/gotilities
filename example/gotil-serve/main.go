package main

import (
	"context"
	"fmt"
	"github.com/gofunct/gotilities/gotility"
	"github.com/gofunct/gotilities/probe"
	pb "github.com/gofunct/gotilities/proto/ping"
	"github.com/prometheus/client_golang/prometheus"
	"net/http"
	"time"
)

// DemoServiceServer defines a Server.
type DemoServiceServer struct{}

func newDemoServer() *DemoServiceServer {
	return &DemoServiceServer{}
}

// SayHello implements a interface defined by protobuf.
func (s *DemoServiceServer) SayHello(ctx context.Context, request *pb.HelloRequest) (*pb.HelloResponse, error) {
	return &pb.HelloResponse{Message: fmt.Sprintf("Hello %s", request.Name)}, nil
}

var g gotility.Gotility

func main() {

	ctx, cancel, errsync := g.MakeErrGrpWithDeadline(120)

	defer cancel()

	logger, err := g.MakeZapper(true)

	g.ZapErr(logger, "failed to create logger", err)

	defer logger.Sync()

	tracer, closer, err := g.MakeTracer("demo", logger)

	g.ZapErr(logger, "failed to create tracer", err)

	defer closer.Close()

	metrics := g.RegGrpcServerMetrics(true)
	probing := g.MakeProbe(prometheus.DefaultRegisterer, "demo")

	probing.AddLivenessCheck("routine_threshold", probe.GoroutineCountCheck(500))

	probing.AddReadinessCheck(
		"google-dnscheck",
		probe.DNSResolveCheck("google.com", 500*time.Millisecond))

	probing.AddReadinessCheck("google-httpcheck",
		probe.HTTPGetCheck("google.com", 500*time.Millisecond))

	// Health check
	probing.AddReadinessCheck(
		"grpc",
		probe.Timeout(func() error { return err }, time.Second*10))

	grpcServer := g.MakeGrpcServer(logger, tracer, metrics)

	pb.RegisterDemoServiceServer(grpcServer, newDemoServer())

	mux := http.NewServeMux()

	debugServer := g.MakeDebugServer(":8080", mux, ctx, grpcServer, probe, logger)

	errsync.Go(func() error { return g.StartDynamicServer("8080", ctx, logger, debugServer, grpcServer) })

	if err := errsync.Wait(); err != nil {
		g.ZapErr(logger, "error sync problem: failed to close server", err)
	}
}
