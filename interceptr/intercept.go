package interceptr

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"net"
	"time"
)

// ServiceInfoProvider is simple wrapper around GetServiceInfo method.
// This interface is implemented by grpc Server.
type ServiceInfoProvider interface {
	// GetServiceInfo returns a map from service names to ServiceInfo.
	// Service names include the package names, in the form of <package>.<service>.
	GetServiceInfo() map[string]grpc.ServiceInfo
}

// RegisterInterceptor preallocates possible dimensions of every metric.
// If peer tracking is enabled, nothing will happen.
// If you register interceptor very frequently (for example during tests) it can allocate huge amount of memory.
func RegisterInterceptor(s ServiceInfoProvider, i *Interceptor) (err error) {
	if i.trackPeers {
		return nil
	}

	infos := s.GetServiceInfo()
	for sn, info := range infos {
		for _, m := range info.Methods {
			t := handlerType(m.IsClientStream, m.IsServerStream)

			for c := uint32(0); c <= 15; c++ {
				requestLabels := prometheus.Labels{
					"service": sn,
					"handler": m.Name,
					"code":    codes.Code(c).String(),
					"type":    t,
				}
				messageLabels := prometheus.Labels{
					"service": sn,
					"handler": m.Name,
				}

				// server
				if _, err = i.monitoring.server.errors.GetMetricWith(requestLabels); err != nil {
					return err
				}
				if _, err = i.monitoring.server.requestsTotal.GetMetricWith(requestLabels); err != nil {
					return err
				}
				if _, err = i.monitoring.server.requestDuration.GetMetricWith(requestLabels); err != nil {
					return err
				}
				if m.IsClientStream {
					if _, err = i.monitoring.server.messagesReceived.GetMetricWith(messageLabels); err != nil {
						return err
					}
				}
				if m.IsServerStream {
					if _, err = i.monitoring.server.messagesSend.GetMetricWith(messageLabels); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// Interceptor ...
type Interceptor struct {
	monitoring *monitoring
	trackPeers bool
}

// InterceptorOpts ...
type InterceptorOpts struct {
	// TrackPeers allow to turn on peer tracking.
	// For more info about peers please visit https://godoc.org/google.golang.org/grpc/peer.
	// peer is not bounded dimension so it can cause performance loss.
	// If its turn on Interceptor will not init metrics on startup.
	TrackPeers bool
}

// NewInterceptor implements both prometheus Collector interface and methods required by grpc Interceptor.
func NewInterceptor(opts InterceptorOpts) *Interceptor {
	return &Interceptor{
		monitoring: initMonitoring(opts.TrackPeers),
		trackPeers: opts.TrackPeers,
	}
}

// Dialer ...
func (i *Interceptor) Dialer(f func(string, time.Duration) (net.Conn, error)) func(string, time.Duration) (net.Conn, error) {
	return func(addr string, timeout time.Duration) (net.Conn, error) {
		i.monitoring.dialer.WithLabelValues(addr).Inc()
		return f(addr, timeout)
	}
}

// UnaryClient ...
func (i *Interceptor) UnaryClient() grpc.UnaryClientInterceptor {
	monitor := i.monitoring.client

	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()

		err := invoker(ctx, method, req, reply, cc, opts...)
		code := grpc.Code(err)
		service, method := split(method)
		labels := prometheus.Labels{
			"service": service,
			"handler": method,
			"code":    code.String(),
			"type":    "unary",
		}
		if err != nil && code != codes.OK {
			monitor.errors.With(labels).Add(1)
		}

		monitor.requestDuration.With(labels).Observe(time.Since(start).Seconds())
		monitor.requestsTotal.With(labels).Add(1)

		return err
	}
}

// StreamClient ...
func (i *Interceptor) StreamClient() grpc.StreamClientInterceptor {
	monitor := i.monitoring.client

	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		start := time.Now()

		client, err := streamer(ctx, desc, cc, method, opts...)
		code := grpc.Code(err)
		service, method := split(method)
		labels := prometheus.Labels{
			"service": service,
			"handler": method,
			"code":    code.String(),
			"type":    handlerType(desc.ClientStreams, desc.ServerStreams),
		}
		if err != nil && code != codes.OK {
			monitor.errors.With(labels).Add(1)
		}

		monitor.requestDuration.With(labels).Observe(time.Since(start).Seconds())
		monitor.requestsTotal.With(labels).Add(1)

		return &monitoredClientStream{ClientStream: client, monitor: monitor, labels: prometheus.Labels{
			"service": service,
			"handler": method,
		}}, err
	}
}

// UnaryServer ...
func (i *Interceptor) UnaryServer() grpc.UnaryServerInterceptor {
	monitor := i.monitoring.server

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()

		res, err := handler(ctx, req)
		code := grpc.Code(err)
		service, method := split(info.FullMethod)

		labels := prometheus.Labels{
			"service": service,
			"handler": method,
			"code":    code.String(),
			"type":    "unary",
		}
		if i.trackPeers {
			labels["peer"] = peerValue(ctx)
		}
		if err != nil && code != codes.OK {
			monitor.errors.With(labels).Add(1)
		}

		monitor.requestDuration.With(labels).Observe(time.Since(start).Seconds())
		monitor.requestsTotal.With(labels).Add(1)

		return res, err
	}
}

// StreamServer ...
func (i *Interceptor) StreamServer() grpc.StreamServerInterceptor {
	monitor := i.monitoring.server

	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()

		service, method := split(info.FullMethod)
		streamLabels := prometheus.Labels{
			"service": service,
			"handler": method,
		}
		if i.trackPeers {
			if ss != nil {
				streamLabels["peer"] = peerValue(ss.Context())
			} else {
				// mostly for testing purposes
				streamLabels["peer"] = "nil-server-stream"
			}
		}
		err := handler(srv, &monitoredServerStream{ServerStream: ss, labels: streamLabels, monitor: monitor})
		code := grpc.Code(err)
		labels := prometheus.Labels{
			"service": service,
			"handler": method,
			"code":    code.String(),
			"type":    handlerType(info.IsClientStream, info.IsServerStream),
		}
		if i.trackPeers {
			if ss != nil {
				labels["peer"] = peerValue(ss.Context())
			} else {
				// mostly for testing purposes
				labels["peer"] = "nil-server-stream"
			}
		}
		if err != nil && code != codes.OK {
			monitor.errors.With(labels).Add(1)
		}

		monitor.requestDuration.With(labels).Observe(time.Since(start).Seconds())
		monitor.requestsTotal.With(labels).Add(1)

		return err
	}
}
