package rpc

import (
	"crypto/tls"
	"fmt"
	"github.com/gogo/status"
	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-middleware/validator"
	"github.com/heptiolabs/healthcheck"
	"github.com/mwitkow/go-conntrack"
	"github.com/mwitkow/go-grpc-middleware/logging/zap"
	"github.com/opentracing/opentracing-go"
	"github.com/piotrkowalczuk/promgrpc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/net/context"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"
)

type Server struct{}

func (g *Server) RegGrpcServerMetrics(trackpeers bool) *promgrpc.Interceptor {
	return promgrpc.NewInterceptor(promgrpc.InterceptorOpts{})
}

func NewServerInterceptors() (
	unaryInterceptor grpc.UnaryServerInterceptor,
	streamInterceptor grpc.StreamServerInterceptor,
) {
	unaryInterceptor = func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		_, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, grpc.Errorf(codes.Unauthenticated, "missing context metadata")
		}

		if handler == nil {
			return nil, nil
		}
		return handler(ctx, req)
	}

	streamInterceptor = func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		_, ok := metadata.FromIncomingContext(stream.Context())
		if !ok {
			return grpc.Errorf(codes.Unauthenticated, "missing context metadata")
		}

		if handler == nil {
			return nil
		}
		return handler(srv, stream)
	}

	return
}

func (g *Server) RateLimitingServerInterceptor(r rate.Limit, b int) grpc.UnaryServerInterceptor {
	limiter := rate.NewLimiter(r, b)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := limiter.Wait(ctx); err != nil {
			return nil, status.Error(codes.Canceled, "context exceeded")
		}
		return handler(ctx, req)
	}
}

// CheckClientIsLocal checks that request comes from the local networks
func (g *Server) CheckClientIsLocal(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	localNetworks := []*net.IPNet{
		&net.IPNet{IP: net.IP{0x7f, 0x0, 0x0, 0x0}, Mask: net.IPMask{0xff, 0x0, 0x0, 0x0}},
		&net.IPNet{IP: net.IP{0xa, 0x0, 0x0, 0x0}, Mask: net.IPMask{0xff, 0x0, 0x0, 0x0}},
		&net.IPNet{IP: net.IP{0x64, 0x40, 0x0, 0x0}, Mask: net.IPMask{0xff, 0xc0, 0x0, 0x0}},
		&net.IPNet{IP: net.IP{0xac, 0x10, 0x0, 0x0}, Mask: net.IPMask{0xff, 0xf0, 0x0, 0x0}},
		&net.IPNet{IP: net.IP{0xc0, 0xa8, 0x0, 0x0}, Mask: net.IPMask{0xff, 0xff, 0x0, 0x0}},
	}
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.PermissionDenied, "peer info not-found")
	}
	addr, ok := p.Addr.(*net.TCPAddr)
	if !ok {
		return nil, status.Errorf(codes.PermissionDenied, "broken peer info")
	}
	for _, n := range localNetworks {
		if n.Contains(addr.IP.To4()) {
			return handler(ctx, req)
		}
	}
	return nil, status.Errorf(codes.PermissionDenied, "Request must be from local. Your IP: %s", addr.IP)
}

func (g *Server) MakeGrpcTLSServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor, tls credentials.TransportCredentials) *grpc.Server {

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
				grpc_recovery.UnaryServerInterceptor(),
			),
		),
	)
}

func (g *Server) MakeGrpcServer(l *zap.Logger, tracer opentracing.Tracer, icpt *promgrpc.Interceptor) *grpc.Server {

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

func (g *Server) MakeDebugServer(mux *http.ServeMux, ctx context.Context, server *grpc.Server, check healthcheck.Handler, logger *zap.Logger) *http.Server {

	mux.HandleFunc("/ready", check.ReadyEndpoint)
	mux.HandleFunc("/live", check.LiveEndpoint)
	mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
	mux.Handle("/metrics", promhttp.Handler())

	return &http.Server{
		Handler: mux,
	}
}

func (g *Server) StartDebugger(listener net.Listener, ctx context.Context, logger *zap.Logger, server *http.Server) error {

	e := make(chan error)

	go func() {

		logger.Debug("starting debug server")

		e <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Debug("closing debug server")
		server.Shutdown(ctx)
		return nil

	case err := <-e:
		return err
	}
}

func (g *Server) StartRpcServer(listener net.Listener, ctx context.Context, logger *zap.Logger, server *grpc.Server) error {

	e := make(chan error)

	go func() {

		logger.Debug("starting rpc server")

		e <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		logger.Debug("closing grpc server")
		return nil

	case err := <-e:
		return err
	}
}
func (g *Server) StartDynamicTLSServer(port string, ctx context.Context, logger *zap.Logger, server *http.Server, tlspath string, certpath string, gserver *grpc.Server) error {

	conn, err := net.Listen("tcp", fmt.Sprintf(":%v", port))

	if err != nil {
		return err
	}

	conn = conntrack.NewListener(conn, conntrack.TrackWithTracing())

	tlsConfig, err := g.TlsConfigForServerCerts(certpath, tlspath)

	if err != nil {
		logger.Fatal("Failed configuring TLS: %v", zap.Error(err))
	}

	tlsConfig, err = g.TlsConfigWithHttp2Enabled(tlsConfig)
	if err != nil {
		logger.Fatal("Failed configuring TLS: %v", zap.Error(err))
	}

	logger.Debug("Listening with TLS")

	tlsListener := tls.NewListener(conn, tlsConfig)
	conn = tlsListener

	e := make(chan error)

	go func() {

		logger.Debug("starting secure server", zap.String("addr", port))
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

// TlsConfigForServerCerts is a returns a simple `tls.Config` with the given server cert loaded.
// This is useful if you can't use `http.ListenAndServerTLS` when using a custom `net.Listener`.
func (g *Server) TlsConfigForServerCerts(certFile string, keyFile string) (*tls.Config, error) {
	var err error
	config := new(tls.Config)
	config.Certificates = make([]tls.Certificate, 1)
	config.Certificates[0], err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return config, nil
}

// TlsConfigWithHttp2Enabled makes it easy to configure the given `tls.Config` to prefer H2 connections.
// This is useful if you can't use `http.ListenAndServerTLS` when using a custom `net.Listener`.
func (g *Server) TlsConfigWithHttp2Enabled(config *tls.Config) (*tls.Config, error) {
	// mostly based on http2 code in the standards library.
	if config.CipherSuites != nil {
		// If they already provided a CipherSuite list, return
		// an error if it has a bad order or is missing
		// ECDHE_RSA_WITH_AES_128_GCM_SHA256.
		const requiredCipher = tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		haveRequired := false
		for _, cs := range config.CipherSuites {
			if cs == requiredCipher {
				haveRequired = true
			}
		}
		if !haveRequired {
			return nil, fmt.Errorf("http2: TLSConfig.CipherSuites is missing HTTP/2-required TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256")
		}
	}

	config.PreferServerCipherSuites = true

	haveNPN := false
	for _, p := range config.NextProtos {
		if p == "h2" {
			haveNPN = true
			break
		}
	}
	if !haveNPN {
		config.NextProtos = append(config.NextProtos, "h2")
	}
	config.NextProtos = append(config.NextProtos, "h2-14")
	// make sure http 1.1 is *after* all of the other ones.
	config.NextProtos = append(config.NextProtos, "http/1.1")
	return config, nil
}

// grpcHandlerFunc returns an http.Handler that delegates to grpcServer on incoming gRPC
// connections or otherHandler otherwise. Copied from cockroachdb.
func (g *Server) GrpcHandlerFunc(grpcServer *grpc.Server, otherHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			otherHandler.ServeHTTP(w, r)
		}
	})
}
