package gotilities

import (
	"context"
	"github.com/opentracing/opentracing-go"
	"github.com/piotrkowalczuk/promgrpc"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io"
	"net/http"
	"time"
)

//Gotility implements the Utility interface
type Gotility struct {
	Namespace    string
	Dependencies []string
	Probe        Handler
	Port         string
	Version      string
	Labels       []map[string]string
	CertKey      string
	PrivKey      string
}

type Utility interface {
	MakeZapper() (*zap.Logger, error)
	MakeMetrics(trackpeers bool) *promgrpc.Interceptor
	MakeTracer(service string, logger *zap.Logger) (opentracing.Tracer, io.Closer, error)
	MakeProbe(registry prometheus.Registerer, namespace string) Handler
	MakeErrGrp() (*Group, context.Context)
	MakeErrGrpWithDeadline(duration time.Duration) (context.Context, context.CancelFunc, *Group)
	ZapErr(logger *zap.Logger, msg string, err error)
	MakeGrpcServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor) *grpc.Server
	MakeGrpcTLSServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor, credentials credentials.TransportCredentials) *grpc.Server
	MakeDebugServer(mux *http.ServeMux, ctx context.Context, server *grpc.Server, health Handler, logger *zap.Logger) *http.Server
	StartDynamicServer(port string, ctx context.Context, logger *zap.Logger, server *http.Server, gserver *grpc.Server) error
	StartDynamicTLSServer(port string, ctx context.Context, logger *zap.Logger, server *http.Server, tls string, cert string, gserver *grpc.Server) error
}
