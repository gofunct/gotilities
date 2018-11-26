package rpc

import (
	"fmt"
	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/opentracing-contrib/go-grpc"
	"github.com/opentracing/opentracing-go"
	"github.com/piotrkowalczuk/promgrpc"
	"google.golang.org/grpc"
	"net"
	"time"
)

type ClientUtil interface {
	MakeGrpcClient(address string, tracer opentracing.Tracer, interceptor *promgrpc.Interceptor) (*grpc.ClientConn, error)
}

type Client struct{}

func (c *Client) MakeGrpcClient(port string, tracer opentracing.Tracer, interceptor *promgrpc.Interceptor) (*grpc.ClientConn, error) {

	conn, err := grpc.Dial(
		fmt.Sprintf(":%v", port),
		grpc.WithInsecure(),
		grpc.WithStatsHandler(interceptor),
		grpc.WithDialer(interceptor.Dialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		})),
		grpc.WithUnaryInterceptor(grpc_middleware.ChainUnaryClient(
			interceptor.UnaryClient(),
			otgrpc.OpenTracingClientInterceptor(tracer),
		)),

		grpc.WithStreamInterceptor(grpc_middleware.ChainStreamClient(
			interceptor.StreamClient()),
		),
	)
	if err != nil {
		return nil, err
	}

	return conn, err
}
