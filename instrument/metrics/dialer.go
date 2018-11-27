package metrics

import (
	"context"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/trace"
	"net"
	"os"
	"syscall"
)

func reportDialerConnAttempt(name string) {
	dialerAttemptedTotal.WithLabelValues(name).Inc()
}

func reportDialerConnEstablished(name string) {
	dialerConnEstablishedTotal.WithLabelValues(name).Inc()
}

func reportDialerConnClosed(name string) {
	dialerConnClosedTotal.WithLabelValues(name).Inc()
}

func reportDialerConnFailed(name string, err error) {
	if netErr, ok := err.(*net.OpError); ok {
		switch nestErr := netErr.Err.(type) {
		case *net.DNSError:
			dialerConnFailedTotal.WithLabelValues(name, string(failedResolution)).Inc()
			return
		case *os.SyscallError:
			if nestErr.Err == syscall.ECONNREFUSED {
				dialerConnFailedTotal.WithLabelValues(name, string(failedConnRefused)).Inc()
			}
			dialerConnFailedTotal.WithLabelValues(name, string(failedUnknown)).Inc()
			return
		}
		if netErr.Timeout() {
			dialerConnFailedTotal.WithLabelValues(name, string(failedTimeout)).Inc()
		}
	} else if err == context.Canceled || err == context.DeadlineExceeded {
		dialerConnFailedTotal.WithLabelValues(name, string(failedTimeout)).Inc()
		return
	}
	dialerConnFailedTotal.WithLabelValues(name, string(failedUnknown)).Inc()
}

func DialNameFromContext(ctx context.Context) string {
	val, ok := ctx.Value(dialerNameKey).(string)
	if !ok {
		return ""
	}
	return val
}

func WrapDialer(name string, monitoring, tracing bool, reg *prometheus.Registry) func(context.Context, string, string) (net.Conn, error) {
	opts := &dialerOpts{
		name:                  name,
		monitoring:            monitoring,
		tracing:               tracing,
		parentDialContextFunc: (&net.Dialer{}).DialContext,
	}

	go func() {

		reg.MustRegister(dialerAttemptedTotal)
		reg.MustRegister(dialerConnEstablishedTotal)
		reg.MustRegister(dialerConnFailedTotal)
		reg.MustRegister(dialerConnClosedTotal)

		dialerAttemptedTotal.WithLabelValues(name)
		dialerConnEstablishedTotal.WithLabelValues(name)
		for _, reason := range []failureReason{failedTimeout, failedResolution, failedConnRefused, failedUnknown} {
			dialerConnFailedTotal.WithLabelValues(name, string(reason))
		}

		dialerConnClosedTotal.WithLabelValues(name)

	}()

	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		name := opts.name
		if ctxName := DialNameFromContext(ctx); ctxName != "" {
			name = ctxName
		}
		return dialClientConnTracker(ctx, network, addr, name, opts)
	}
}

func dialClientConnTracker(ctx context.Context, network string, addr string, name string, opts *dialerOpts) (net.Conn, error) {
	var event trace.EventLog
	if opts.tracing {
		event = trace.NewEventLog(fmt.Sprintf("net.ClientConn.%s", name), fmt.Sprintf("%v", addr))
	}
	if opts.monitoring {
		reportDialerConnAttempt(name)
	}
	conn, err := opts.parentDialContextFunc(ctx, network, addr)
	if err != nil {
		if event != nil {
			event.Errorf("failed dialing: %v", err)
			event.Finish()
		}
		if opts.monitoring {
			reportDialerConnFailed(name, err)
		}
		return nil, err
	}
	if event != nil {
		event.Printf("established: %s -> %s", conn.LocalAddr(), conn.RemoteAddr())
	}
	if opts.monitoring {
		reportDialerConnEstablished(name)
	}
	tracker := &clientConnTracker{
		Conn:  conn,
		opts:  opts,
		name:  name,
		event: event,
	}
	return tracker, nil
}

func (ct *clientConnTracker) Close() error {
	err := ct.Conn.Close()
	ct.mu.Lock()
	if ct.event != nil {
		if err != nil {
			ct.event.Errorf("failed closing: %v", err)
		} else {
			ct.event.Printf("closing")
		}
		ct.event.Finish()
		ct.event = nil
	}
	ct.mu.Unlock()
	if ct.opts.monitoring {
		reportDialerConnClosed(ct.name)
	}
	return err
}
