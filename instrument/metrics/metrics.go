package metrics

import (
	"context"
	prom "github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/trace"
	"net"
	"sync"
	"time"
)

type failureReason string

const (
	failedResolution  = "resolution"
	failedConnRefused = "refused"
	failedTimeout     = "timeout"
	failedUnknown     = "unknown"
)

var (
	dialerNameKey = "DialerKey"
)

type dialerOpts struct {
	name                  string
	monitoring            bool
	tracing               bool
	parentDialContextFunc dialerContextFunc
}

type dialerContextFunc func(context.Context, string, string) (net.Conn, error)

type listenerOpts struct {
	name         string
	monitoring   bool
	tracing      bool
	tcpKeepAlive time.Duration
}

type serverConnTracker struct {
	net.Conn
	opts  *listenerOpts
	event trace.EventLog
	mu    sync.Mutex
}

type connTrackListener struct {
	net.Listener
	opts *listenerOpts
}

type clientConnTracker struct {
	net.Conn
	opts  *dialerOpts
	name  string
	event trace.EventLog
	mu    sync.Mutex
}

var (
	listenerAcceptedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "listener",
			Name:      "listener_conn_accepted_total",
			Help:      "Total number of connections opened to the listener of a given name.",
		}, []string{"listener_name"})

	listenerClosedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "listener",
			Name:      "listener_conn_closed_total",
			Help:      "Total number of connections closed that were made to the listener of a given name.",
		}, []string{"listener_name"})

	dialerAttemptedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "dialer",
			Name:      "dialer_conn_attempted_total",
			Help:      "Total number of connections attempted by the given dialer a given name.",
		}, []string{"dialer_name"})

	dialerConnEstablishedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "dialer",
			Name:      "dialer_conn_established_total",
			Help:      "Total number of connections successfully established by the given dialer a given name.",
		}, []string{"dialer_name"})

	dialerConnFailedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "dialer",
			Name:      "dialer_conn_failed_total",
			Help:      "Total number of connections failed to dial by the dialer a given name.",
		}, []string{"dialer_name", "reason"})

	dialerConnClosedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "dialer",
			Name:      "dialer_conn_closed_total",
			Help:      "Total number of connections closed which originated from the dialer of a given name.",
		}, []string{"dialer_name"})
)
