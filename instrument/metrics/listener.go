package metrics

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/trace"
	"net"
)

func WrapListener(old net.Listener, reg *prometheus.Registry, name string, monitoring, tracing bool) net.Listener {
	opts := &listenerOpts{
		name:       name,
		monitoring: monitoring,
		tracing:    tracing,
	}

	go func() {
		reg.MustRegister(listenerAcceptedTotal)
		reg.MustRegister(listenerClosedTotal)
		listenerAcceptedTotal.WithLabelValues(name)
		listenerClosedTotal.WithLabelValues(name)
	}()

	return &connTrackListener{
		Listener: old,
		opts:     opts,
	}
}

func (ct *connTrackListener) Accept() (net.Conn, error) {
	conn, err := ct.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok && ct.opts.tcpKeepAlive > 0 {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(ct.opts.tcpKeepAlive)
	}
	return newServerConnTracker(conn, ct.opts), nil
}

func reportListenerConnAccepted(listenerName string) {
	listenerAcceptedTotal.WithLabelValues(listenerName).Inc()
}

func reportListenerConnClosed(listenerName string) {
	listenerClosedTotal.WithLabelValues(listenerName).Inc()
}

func newServerConnTracker(inner net.Conn, opts *listenerOpts) net.Conn {

	tracker := &serverConnTracker{
		Conn: inner,
		opts: opts,
	}
	if opts.tracing {
		tracker.event = trace.NewEventLog(fmt.Sprintf("net.ServerConn.%s", opts.name), fmt.Sprintf("%v", inner.RemoteAddr()))
		tracker.event.Printf("accepted: %v -> %v", inner.RemoteAddr(), inner.LocalAddr())
	}
	if opts.monitoring {
		reportListenerConnAccepted(opts.name)
	}
	return tracker
}

func (ct *serverConnTracker) Close() error {
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
		reportListenerConnClosed(ct.opts.name)
	}
	return err
}
