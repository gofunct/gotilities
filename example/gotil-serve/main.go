package main

import (
	"context"
	"fmt"
	"github.com/gofunct/gotilities"
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

var gotility gotilities.Gotility

func main() {

	ctx, cancel, errsync := gotility.MakeErrGrpWithDeadline(120)

	defer cancel()

	logger, err := gotility.MakeZapper(true)

	gotility.ZapErr(logger, "failed to create logger", err)

	defer logger.Sync()

	tracer, closer, err := gotility.MakeTracer("demo", logger)

	gotility.ZapErr(logger, "failed to create tracer", err)

	defer closer.Close()

	metrics := gotility.RegGrpcServerMetrics(true)
	probe := gotility.MakeProbe(prometheus.DefaultRegisterer, "demo")

	probe.AddLivenessCheck("routine_threshold", gotilities.GoroutineCountCheck(500))

	probe.AddReadinessCheck(
		"google-dnscheck",
		gotilities.DNSResolveCheck("google.com", 500*time.Millisecond))

	probe.AddReadinessCheck("google-httpcheck",
		gotilities.HTTPGetCheck("google.com", 500*time.Millisecond))

	// Health check
	probe.AddReadinessCheck(
		"grpc",
		gotilities.Timeout(func() error { return err }, time.Second*10))

	grpcServer := gotility.MakeGrpcServer(logger, tracer, metrics)

	pb.RegisterDemoServiceServer(grpcServer, newDemoServer())

	mux := http.NewServeMux()

	debugServer := gotility.MakeDebugServer(":8080", mux, ctx, grpcServer, probe, logger)

	errsync.Go(func() error { return gotility.StartDynamicServer("8080", ctx, logger, debugServer, grpcServer) })

	if err := errsync.Wait(); err != nil {
		gotility.ZapErr(logger, "error sync problem: failed to close server", err)
	}
}
