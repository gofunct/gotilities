package gotilities

import (
	"context"
	"fmt"
	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-middleware/validator"
	"github.com/mwitkow/go-grpc-middleware/logging/zap"
	"github.com/opentracing/opentracing-go"
	"github.com/piotrkowalczuk/promgrpc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

func (g *Gotility) MakeGrpcTLSServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor, tls credentials.TransportCredentials) *grpc.Server {

	unaryInterceptor, _ := NewServerInterceptors()

	logger := l.Sugar().Desugar().Named("grpc")

	grpc_zap.ReplaceGrpcLogger(logger)

	zapOpts := []grpc_zap.Option{
		grpc_zap.WithDurationField(func(duration time.Duration) zapcore.Field {
			return zap.Duration("grpc.duration", duration)
		}),
	}
	return grpc.NewServer(
		grpc.StatsHandler(icpt),
		grpc.Creds(tls),
		grpc.UnaryInterceptor(
			grpc_middleware.ChainUnaryServer(
				unaryInterceptor,
				grpc_ctxtags.UnaryServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
				grpc_zap.UnaryServerInterceptor(logger, zapOpts...),
				grpc_validator.UnaryServerInterceptor(),
				icpt.UnaryServer(),
			),
		),
	)
}

func (g *Gotility) MakeGrpcServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor) *grpc.Server {

	unaryInterceptor, _ := NewServerInterceptors()

	logger := l.Sugar().Desugar().Named("grpc")

	grpc_zap.ReplaceGrpcLogger(logger)

	zapOpts := []grpc_zap.Option{
		grpc_zap.WithDurationField(func(duration time.Duration) zapcore.Field {
			return zap.Duration("grpc.duration", duration)
		}),
	}
	s := grpc.NewServer(
		grpc.StatsHandler(icpt),
		grpc.UnaryInterceptor(
			grpc_middleware.ChainUnaryServer(
				unaryInterceptor,
				grpc_ctxtags.UnaryServerInterceptor(grpc_ctxtags.WithFieldExtractor(grpc_ctxtags.CodeGenRequestFieldExtractor)),
				grpc_zap.UnaryServerInterceptor(logger, zapOpts...),
				grpc_validator.UnaryServerInterceptor(),
				icpt.UnaryServer(),
			),
		),
	)
	prometheus.DefaultRegisterer.Register(icpt)
	promgrpc.RegisterInterceptor(s, icpt)
	grpc_health_v1.RegisterHealthServer(s, health.NewServer())

	return s

}

func (g *Gotility) MakeDebugServer(mux *http.ServeMux, ctx context.Context, server *grpc.Server, check Handler, logger *zap.Logger) *http.Server {

	mux.HandleFunc("/ready", check.ReadyEndpoint)
	mux.HandleFunc("/live", check.LiveEndpoint)
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	mux.Handle("/metrics", promhttp.Handler())

	return &http.Server{
		Addr:    g.Port,
		Handler: GrpcHandlerFunc(server, mux),
	}
}

func (g *Gotility) StartDynamicServer(port string, ctx context.Context, logger *zap.Logger, server *http.Server, gserver *grpc.Server) error {
	conn, err := net.Listen("tcp", fmt.Sprintf(":%v", port))

	if err != nil {
		return err
	}

	e := make(chan error)

	go func() {

		logger.Debug("starting server", zap.String("addr", port))
		e <- server.Serve(conn)
	}()

	select {
	case <-ctx.Done():
		logger.Debug("closing server", zap.String("addr", port))
		gserver.GracefulStop()
		server.Shutdown(ctx)
		return nil

	case err := <-e:
		return err
	}
}

func (g *Gotility) StartDynamicTLSServer(port string, ctx context.Context, logger *zap.Logger, server *http.Server, tls string, cert string, gserver *grpc.Server) error {

	conn, err := net.Listen("tcp", fmt.Sprintf(":%v", port))

	if err != nil {
		return err
	}

	e := make(chan error)

	go func() {

		logger.Debug("starting secure server", zap.String("addr", port))
		e <- server.ServeTLS(conn, tls, cert)
	}()

	select {
	case <-ctx.Done():
		logger.Debug("closing server", zap.String("addr", port))
		gserver.GracefulStop()
		server.Shutdown(ctx)

		return nil

	case err := <-e:
		return err
	}

}
