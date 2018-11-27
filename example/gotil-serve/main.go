package main

import (
	"context"
	"fmt"
	"github.com/gofunct/gotilities/gotility"
	"github.com/gofunct/gotilities/instrument/metrics"
	pb "github.com/gofunct/gotilities/proto/ping"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/heptiolabs/healthcheck"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/soheilhy/cmux"
	"net"
	"net/http"
	"time"
)

var(
	reg = prometheus.NewRegistry()
	grpcMetrics = grpc_prometheus.NewServerMetrics()

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
	reg.MustRegister(grpcMetrics)

	ctx, cancel, errsync := g.MakeErrGrpWithDeadline(120)

	defer cancel()

	logger, err := g.MakeZapper(true)

	g.ZapErr(logger, "failed to create logger", err)

	defer logger.Sync()

	tracer, closer, err := g.MakeTracer("demo", logger)

	g.ZapErr(logger, "failed to create tracer", err)

	defer closer.Close()

	probe := healthcheck.NewMetricsHandler(prometheus.DefaultRegisterer, "demo")

	probe.AddLivenessCheck("routine_threshold", healthcheck.GoroutineCountCheck(500))

	probe.AddReadinessCheck(
		"google-dnscheck",
		healthcheck.DNSResolveCheck("google.com", 500*time.Millisecond))

	probe.AddReadinessCheck("google-httpcheck",
		healthcheck.HTTPGetCheck("google.com", 500*time.Millisecond))

	// Health check
	probe.AddReadinessCheck(
		"grpc",
		healthcheck.Timeout(func() error { return err }, time.Second*10))

	grpcServer := g.MakeGrpcServer(logger, tracer)

	pb.RegisterDemoServiceServer(grpcServer, newDemoServer())

	conn, err := net.Listen("tcp", fmt.Sprintf(":%v", "8080"))

	g.ZapErr(logger, "failed to create listener", err)

	//register listener metrics
	conn = metrics.WrapListener(conn, reg, "IAM:cert", true, true)
	// Initialize all metrics.
	grpcMetrics.InitializeMetrics(grpcServer)
	m := http.NewServeMux()

	mux := cmux.New(conn)

	httpLis := mux.Match(cmux.HTTP1Fast())
	grpcLis := mux.Match(cmux.HTTP2HeaderFieldPrefix("content-type", "application/grpc"))

	debugServer := g.MakeDebugServer(m, ctx, grpcServer, probe, logger)

	errsync.Go(func() error { return g.StartDebugger(httpLis, ctx, logger, debugServer) })
	errsync.Go(func() error { return g.StartRpcServer(grpcLis, ctx, logger, grpcServer) })
	errsync.Go(func() error { return mux.Serve() })

	if err := errsync.Wait(); err != nil {
		g.ZapErr(logger, "error sync problem: failed to close server", err)
	}
}
