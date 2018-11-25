package gotilities

import (
	"github.com/gogo/status"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"
	"golang.org/x/net/context"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"net"
)

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

// ClientInterceptor grpc client wrapper
func ClientJaegerInterceptor(tracer opentracing.Tracer) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string,
		req, reply interface{}, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {

		var parentCtx opentracing.SpanContext
		parentSpan := opentracing.SpanFromContext(ctx)
		if parentSpan != nil {
			parentCtx = parentSpan.Context()
		}

		span := tracer.StartSpan(
			method,
			opentracing.ChildOf(parentCtx),
			opentracing.Tag{Key: string(ext.Component), Value: "gRPC"},
			ext.SpanKindRPCClient,
		)
		defer span.Finish()

		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		} else {
			md = md.Copy()
		}

		mdWriter := MDReaderWriter{md}
		err := tracer.Inject(span.Context(), opentracing.TextMap, mdWriter)
		if err != nil {
			span.LogFields(log.String("inject-error", err.Error()))
		}

		newCtx := metadata.NewOutgoingContext(ctx, md)
		err = invoker(newCtx, method, req, reply, cc, opts...)
		if err != nil {
			span.LogFields(log.String("call-error", err.Error()))
		}
		return err
	}
}

// ServerInterceptor grpc server wrapper
func ServerJaegerInterceptor(tracer opentracing.Tracer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (resp interface{}, err error) {

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.New(nil)
		}

		spanContext, err := tracer.Extract(opentracing.TextMap, MDReaderWriter{md})
		if err != nil && err != opentracing.ErrSpanContextNotFound {
			grpclog.Errorf("extract from metadata err: %v", err)
		} else {
			span := tracer.StartSpan(
				info.FullMethod,
				ext.RPCServerOption(spanContext),
				opentracing.Tag{Key: string(ext.Component), Value: "gRPC"},
				ext.SpanKindRPCServer,
			)
			defer span.Finish()

			ctx = opentracing.ContextWithSpan(ctx, span)
		}

		return handler(ctx, req)
	}
}

func (g *Gotility) RateLimitingServerInterceptor(r rate.Limit, b int) grpc.UnaryServerInterceptor {
	limiter := rate.NewLimiter(r, b)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := limiter.Wait(ctx); err != nil {
			return nil, status.Error(codes.Canceled, "context exceeded")
		}
		return handler(ctx, req)
	}
}

// CheckClientIsLocal checks that request comes from the local networks
func (g *Gotility) CheckClientIsLocal(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
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
