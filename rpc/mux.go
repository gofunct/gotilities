package rpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	prom "github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"golang.org/x/net/trace"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Matcher matches a connection based on its content.
type Matcher func(io.Reader) bool

// MatchWriter is a match that can also write response (say to do handshake).
type MatchWriter func(io.Writer, io.Reader) bool

// ErrorHandler handles an error and returns whether
// the mux should continue serving the listener.
type ErrorHandler func(error) bool

var _ net.Error = ErrNotMatched{}

// ErrNotMatched is returned whenever a connection is not matched by any of
// the matchers registered in the multiplexer.
type ErrNotMatched struct {
	c net.Conn
}

func (e ErrNotMatched) Error() string {
	return fmt.Sprintf("mux: connection %v not matched by an matcher",
		e.c.RemoteAddr())
}

// Temporary implements the net.Error interface.
func (e ErrNotMatched) Temporary() bool { return true }

// Timeout implements the net.Error interface.
func (e ErrNotMatched) Timeout() bool { return false }

type errListenerClosed string

func (e errListenerClosed) Error() string   { return string(e) }
func (e errListenerClosed) Temporary() bool { return false }
func (e errListenerClosed) Timeout() bool   { return false }

// ErrListenerClosed is returned from muxListener.Accept when the underlying
// listener is closed.
var ErrListenerClosed = errListenerClosed("mux: listener closed")

// for readability of readTimeout
var noTimeout time.Duration

// New instantiates a new connection multiplexer.
func New(l net.Listener) RpcMux {
	return &rpcMux{
		root:        l,
		bufLen:      1024,
		errh:        func(_ error) bool { return true },
		donec:       make(chan struct{}),
		readTimeout: noTimeout,
	}
}

// RpcMux is a multiplexer for network connections.
type RpcMux interface {
	// Match returns a net.Listener that sees (i.e., accepts) only
	// the connections matched by at least one of the matcher.
	//
	// The order used to call Match determines the priority of matchers.
	Match(...Matcher) net.Listener
	// MatchWithWriters returns a net.Listener that accepts only the
	// connections that matched by at least of the matcher writers.
	//
	// Prefer Matchers over MatchWriters, since the latter can write on the
	// connection before the actual handler.
	//
	// The order used to call Match determines the priority of matchers.
	MatchWithWriters(...MatchWriter) net.Listener
	// Serve starts multiplexing the listener. Serve blocks and perhaps
	// should be invoked concurrently within a go routine.
	Serve() error
	// HandleError registers an error handler that handles listener errors.
	HandleError(ErrorHandler)
	// sets a timeout for the read of matchers
	SetReadTimeout(time.Duration)
}

type matchersListener struct {
	ss []MatchWriter
	l  muxListener
}

type rpcMux struct {
	root        net.Listener
	bufLen      int
	errh        ErrorHandler
	donec       chan struct{}
	sls         []matchersListener
	readTimeout time.Duration
}

func matchersToMatchWriters(matchers []Matcher) []MatchWriter {
	mws := make([]MatchWriter, 0, len(matchers))
	for _, m := range matchers {
		cm := m
		mws = append(mws, func(w io.Writer, r io.Reader) bool {
			return cm(r)
		})
	}
	return mws
}

func (m *rpcMux) Match(matchers ...Matcher) net.Listener {
	mws := matchersToMatchWriters(matchers)
	return m.MatchWithWriters(mws...)
}

func (m *rpcMux) MatchWithWriters(matchers ...MatchWriter) net.Listener {
	ml := muxListener{
		Listener: m.root,
		connc:    make(chan net.Conn, m.bufLen),
	}
	m.sls = append(m.sls, matchersListener{ss: matchers, l: ml})
	return ml
}

func (m *rpcMux) SetReadTimeout(t time.Duration) {
	m.readTimeout = t
}

func (m *rpcMux) Serve() error {
	var wg sync.WaitGroup

	defer func() {
		close(m.donec)
		wg.Wait()

		for _, sl := range m.sls {
			close(sl.l.connc)
			// Drain the connections enqueued for the listener.
			for c := range sl.l.connc {
				_ = c.Close()
			}
		}
	}()

	for {
		c, err := m.root.Accept()
		if err != nil {
			if !m.handleErr(err) {
				return err
			}
			continue
		}

		wg.Add(1)
		go m.serve(c, m.donec, &wg)
	}
}

func (m *rpcMux) serve(c net.Conn, donec <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()

	muc := newMuxConn(c)
	if m.readTimeout > noTimeout {
		_ = c.SetReadDeadline(time.Now().Add(m.readTimeout))
	}
	for _, sl := range m.sls {
		for _, s := range sl.ss {
			matched := s(muc.Conn, muc.startSniffing())
			if matched {
				muc.doneSniffing()
				if m.readTimeout > noTimeout {
					_ = c.SetReadDeadline(time.Time{})
				}
				select {
				case sl.l.connc <- muc:
				case <-donec:
					_ = c.Close()
				}
				return
			}
		}
	}

	_ = c.Close()
	err := ErrNotMatched{c: c}
	if !m.handleErr(err) {
		_ = m.root.Close()
	}
}

func (m *rpcMux) HandleError(h ErrorHandler) {
	m.errh = h
}

func (m *rpcMux) handleErr(err error) bool {
	if !m.errh(err) {
		return false
	}

	if ne, ok := err.(net.Error); ok {
		return ne.Temporary()
	}

	return false
}

type muxListener struct {
	net.Listener
	connc chan net.Conn
}

func (l muxListener) Accept() (net.Conn, error) {
	c, ok := <-l.connc
	if !ok {
		return nil, ErrListenerClosed
	}
	return c, nil
}

// MuxConn wraps a net.Conn and provides transparent sniffing of connection data.
type MuxConn struct {
	net.Conn
	buf bufferedReader
}

func newMuxConn(c net.Conn) *MuxConn {
	return &MuxConn{
		Conn: c,
		buf:  bufferedReader{source: c},
	}
}

func (m *MuxConn) Read(p []byte) (int, error) {
	return m.buf.Read(p)
}

func (m *MuxConn) startSniffing() io.Reader {
	m.buf.reset(true)
	return &m.buf
}

func (m *MuxConn) doneSniffing() {
	m.buf.reset(false)
}

func init() {
	prom.MustRegister(dialerAttemptedTotal)
	prom.MustRegister(dialerConnEstablishedTotal)
	prom.MustRegister(dialerConnFailedTotal)
	prom.MustRegister(dialerConnClosedTotal)
}

type failureReason string

const (
	failedResolution  = "resolution"
	failedConnRefused = "refused"
	failedTimeout     = "timeout"
	failedUnknown     = "unknown"
)

var (
	dialerNameKey = "conntrackDialerKey"
)

type dialerOpts struct {
	name                  string
	monitoring            bool
	tracing               bool
	parentDialContextFunc dialerContextFunc
}

type dialerOpt func(*dialerOpts)

type dialerContextFunc func(context.Context, string, string) (net.Conn, error)

var (
	dialerAttemptedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "dialer_conn_attempted_total",
			Help:      "Total number of connections attempted by the given dialer a given name.",
		}, []string{"dialer_name"})

	dialerConnEstablishedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "dialer_conn_established_total",
			Help:      "Total number of connections successfully established by the given dialer a given name.",
		}, []string{"dialer_name"})

	dialerConnFailedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "dialer_conn_failed_total",
			Help:      "Total number of connections failed to dial by the dialer a given name.",
		}, []string{"dialer_name", "reason"})

	dialerConnClosedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "dialer_conn_closed_total",
			Help:      "Total number of connections closed which originated from the dialer of a given name.",
		}, []string{"dialer_name"})
)

// preRegisterDialerMetrics pre-populates Prometheus labels for the given dialer name, to avoid Prometheus missing labels issue.
func PreRegisterDialerMetrics(dialerName string) {
	dialerAttemptedTotal.WithLabelValues(dialerName)
	dialerConnEstablishedTotal.WithLabelValues(dialerName)
	for _, reason := range []failureReason{failedTimeout, failedResolution, failedConnRefused, failedUnknown} {
		dialerConnFailedTotal.WithLabelValues(dialerName, string(reason))
	}
	dialerConnClosedTotal.WithLabelValues(dialerName)
}

func reportDialerConnAttempt(dialerName string) {
	dialerAttemptedTotal.WithLabelValues(dialerName).Inc()
}

func reportDialerConnEstablished(dialerName string) {
	dialerConnEstablishedTotal.WithLabelValues(dialerName).Inc()
}

func reportDialerConnClosed(dialerName string) {
	dialerConnClosedTotal.WithLabelValues(dialerName).Inc()
}

func reportDialerConnFailed(dialerName string, err error) {
	if netErr, ok := err.(*net.OpError); ok {
		switch nestErr := netErr.Err.(type) {
		case *net.DNSError:
			dialerConnFailedTotal.WithLabelValues(dialerName, string(failedResolution)).Inc()
			return
		case *os.SyscallError:
			if nestErr.Err == syscall.ECONNREFUSED {
				dialerConnFailedTotal.WithLabelValues(dialerName, string(failedConnRefused)).Inc()
			}
			dialerConnFailedTotal.WithLabelValues(dialerName, string(failedUnknown)).Inc()
			return
		}
		if netErr.Timeout() {
			dialerConnFailedTotal.WithLabelValues(dialerName, string(failedTimeout)).Inc()
		}
	} else if err == context.Canceled || err == context.DeadlineExceeded {
		dialerConnFailedTotal.WithLabelValues(dialerName, string(failedTimeout)).Inc()
		return
	}
	dialerConnFailedTotal.WithLabelValues(dialerName, string(failedUnknown)).Inc()
}

// DialWithName sets the name of the dialer for tracking and monitoring.
// This is the name for the dialer (default is `default`), but for `NewDialContextFunc` can be overwritten from the
// Context using `DialNameToContext`.
func DialWithName(name string) dialerOpt {
	return func(opts *dialerOpts) {
		opts.name = name
	}
}

// DialWithoutMonitoring turns *off* Prometheus monitoring for this dialer.
func DialWithoutMonitoring() dialerOpt {
	return func(opts *dialerOpts) {
		opts.monitoring = false
	}
}

// DialWithTracing turns *on* the /debug/events tracing of the dial calls.
func DialWithTracing() dialerOpt {
	return func(opts *dialerOpts) {
		opts.tracing = true
	}
}

// DialWithDialer allows you to override the `net.Dialer` instance used to actually conduct the dials.
func DialWithDialer(parentDialer *net.Dialer) dialerOpt {
	return DialWithDialContextFunc(parentDialer.DialContext)
}

// DialWithDialContextFunc allows you to override func gets used for the actual dialing. The default is `net.Dialer.DialContext`.
func DialWithDialContextFunc(parentDialerFunc dialerContextFunc) dialerOpt {
	return func(opts *dialerOpts) {
		opts.parentDialContextFunc = parentDialerFunc
	}
}

// DialNameFromContext returns the name of the dialer from the context of the DialContext func, if any.
func DialNameFromContext(ctx context.Context) string {
	val, ok := ctx.Value(dialerNameKey).(string)
	if !ok {
		return ""
	}
	return val
}

// DialNameToContext returns a context that will contain a dialer name override.
func DialNameToContext(ctx context.Context, dialerName string) context.Context {
	return context.WithValue(ctx, dialerNameKey, dialerName)
}

// NewDialContextFunc returns a `DialContext` function that tracks outbound connections.
// The signature is compatible with `http.Tranport.DialContext` and is meant to be used there.
func NewDialContextFunc(optFuncs ...dialerOpt) func(context.Context, string, string) (net.Conn, error) {
	opts := &dialerOpts{name: "default", monitoring: true, parentDialContextFunc: (&net.Dialer{}).DialContext}
	for _, f := range optFuncs {
		f(opts)
	}
	if opts.monitoring {
		PreRegisterDialerMetrics(opts.name)
	}
	return func(ctx context.Context, network string, addr string) (net.Conn, error) {
		name := opts.name
		if ctxName := DialNameFromContext(ctx); ctxName != "" {
			name = ctxName
		}
		return dialClientConnTracker(ctx, network, addr, name, opts)
	}
}

// NewDialFunc returns a `Dial` function that tracks outbound connections.
// The signature is compatible with `http.Tranport.Dial` and is meant to be used there for Go < 1.7.
func NewDialFunc(optFuncs ...dialerOpt) func(string, string) (net.Conn, error) {
	dialContextFunc := NewDialContextFunc(optFuncs...)
	return func(network string, addr string) (net.Conn, error) {
		return dialContextFunc(context.TODO(), network, addr)
	}
}

type clientConnTracker struct {
	net.Conn
	opts       *dialerOpts
	dialerName string
	event      trace.EventLog
	mu         sync.Mutex
}

func dialClientConnTracker(ctx context.Context, network string, addr string, dialerName string, opts *dialerOpts) (net.Conn, error) {
	var event trace.EventLog
	if opts.tracing {
		event = trace.NewEventLog(fmt.Sprintf("net.ClientConn.%s", dialerName), fmt.Sprintf("%v", addr))
	}
	if opts.monitoring {
		reportDialerConnAttempt(dialerName)
	}
	conn, err := opts.parentDialContextFunc(ctx, network, addr)
	if err != nil {
		if event != nil {
			event.Errorf("failed dialing: %v", err)
			event.Finish()
		}
		if opts.monitoring {
			reportDialerConnFailed(dialerName, err)
		}
		return nil, err
	}
	if event != nil {
		event.Printf("established: %s -> %s", conn.LocalAddr(), conn.RemoteAddr())
	}
	if opts.monitoring {
		reportDialerConnEstablished(dialerName)
	}
	tracker := &clientConnTracker{
		Conn:       conn,
		opts:       opts,
		dialerName: dialerName,
		event:      event,
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
		reportDialerConnClosed(ct.dialerName)
	}
	return err
}

var (
	listenerAcceptedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "listener_conn_accepted_total",
			Help:      "Total number of connections opened to the listener of a given name.",
		}, []string{"listener_name"})

	listenerClosedTotal = prom.NewCounterVec(
		prom.CounterOpts{
			Namespace: "net",
			Subsystem: "conntrack",
			Name:      "listener_conn_closed_total",
			Help:      "Total number of connections closed that were made to the listener of a given name.",
		}, []string{"listener_name"})
)

func init() {
	prom.MustRegister(listenerAcceptedTotal)
	prom.MustRegister(listenerClosedTotal)
}

// preRegisterListener pre-populates Prometheus labels for the given listener name, to avoid Prometheus missing labels issue.
func preRegisterListenerMetrics(listenerName string) {
	listenerAcceptedTotal.WithLabelValues(listenerName)
	listenerClosedTotal.WithLabelValues(listenerName)
}

func reportListenerConnAccepted(listenerName string) {
	listenerAcceptedTotal.WithLabelValues(listenerName).Inc()
}

func reportListenerConnClosed(listenerName string) {
	listenerClosedTotal.WithLabelValues(listenerName).Inc()
}

type listenerOpts struct {
	name         string
	monitoring   bool
	tracing      bool
	tcpKeepAlive time.Duration
}

type listenerOpt func(*listenerOpts)

// TrackWithName sets the name of the Listener for use in tracking and monitoring.
func TrackWithName(name string) listenerOpt {
	return func(opts *listenerOpts) {
		opts.name = name
	}
}

// TrackWithoutMonitoring turns *off* Prometheus monitoring for this listener.
func TrackWithoutMonitoring() listenerOpt {
	return func(opts *listenerOpts) {
		opts.monitoring = false
	}
}

// TrackWithTracing turns *on* the /debug/events tracing of the live listener connections.
func TrackWithTracing() listenerOpt {
	return func(opts *listenerOpts) {
		opts.tracing = true
	}
}

// TrackWithTcpKeepAlive makes sure that any `net.TCPConn` that get accepted have a keep-alive.
// This is useful for HTTP servers in order for, for example laptops, to not use up resources on the
// server while they don't utilise their connection.
// A value of 0 disables it.
func TrackWithTcpKeepAlive(keepalive time.Duration) listenerOpt {
	return func(opts *listenerOpts) {
		opts.tcpKeepAlive = keepalive
	}
}

type connTrackListener struct {
	net.Listener
	opts *listenerOpts
}

// NewListener returns the given listener wrapped in connection tracking listener.
func NewListener(inner net.Listener, optFuncs ...listenerOpt) net.Listener {
	opts := &listenerOpts{
		name:       "default",
		monitoring: true,
		tracing:    false,
	}
	for _, f := range optFuncs {
		f(opts)
	}
	if opts.monitoring {
		preRegisterListenerMetrics(opts.name)
	}
	return &connTrackListener{
		Listener: inner,
		opts:     opts,
	}
}

func (ct *connTrackListener) Accept() (net.Conn, error) {
	// TODO(mwitkow): Add monitoring of failed accept.
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

type serverConnTracker struct {
	net.Conn
	opts  *listenerOpts
	event trace.EventLog
	mu    sync.Mutex
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

type bufferedReader struct {
	source     io.Reader
	buffer     bytes.Buffer
	bufferRead int
	bufferSize int
	sniffing   bool
	lastErr    error
}

func (s *bufferedReader) Read(p []byte) (int, error) {
	if s.bufferSize > s.bufferRead {
		bn := copy(p, s.buffer.Bytes()[s.bufferRead:s.bufferSize])
		s.bufferRead += bn
		return bn, s.lastErr
	} else if !s.sniffing && s.buffer.Cap() != 0 {
		s.buffer = bytes.Buffer{}
	}
	sn, sErr := s.source.Read(p)
	if sn > 0 && s.sniffing {
		s.lastErr = sErr
		if wn, wErr := s.buffer.Write(p[:sn]); wErr != nil {
			return wn, wErr
		}
	}
	return sn, sErr
}

func (s *bufferedReader) reset(snif bool) {
	s.sniffing = snif
	s.bufferRead = 0
	s.bufferSize = s.buffer.Len()
}

func Any() Matcher {
	return func(r io.Reader) bool { return true }
}

func PrefixMatcher(strs ...string) Matcher {
	pt := newTreeString(strs...)
	return pt.matchPrefix
}

func prefixByteMatcher(list ...[]byte) Matcher {
	pt := newTree(list...)
	return pt.matchPrefix
}

var defaultHTTPMethods = []string{
	"OPTIONS",
	"GET",
	"HEAD",
	"POST",
	"PUT",
	"DELETE",
	"TRACE",
	"CONNECT",
}

func HTTP1Fast(extMethods ...string) Matcher {
	return PrefixMatcher(append(defaultHTTPMethods, extMethods...)...)
}

func TLS(versions ...int) Matcher {
	if len(versions) == 0 {
		versions = []int{
			tls.VersionSSL30,
			tls.VersionTLS10,
			tls.VersionTLS11,
			tls.VersionTLS12,
		}
	}
	prefixes := [][]byte{}
	for _, v := range versions {
		prefixes = append(prefixes, []byte{22, byte(v >> 8 & 0xff), byte(v & 0xff)})
	}
	return prefixByteMatcher(prefixes...)
}

const maxHTTPRead = 4096

func HTTP1() Matcher {
	return func(r io.Reader) bool {
		br := bufio.NewReader(&io.LimitedReader{R: r, N: maxHTTPRead})
		l, part, err := br.ReadLine()
		if err != nil || part {
			return false
		}

		_, _, proto, ok := parseRequestLine(string(l))
		if !ok {
			return false
		}

		v, _, ok := http.ParseHTTPVersion(proto)
		return ok && v == 1
	}
}

// grabbed from net/http.
func parseRequestLine(line string) (method, uri, proto string, ok bool) {
	s1 := strings.Index(line, " ")
	s2 := strings.Index(line[s1+1:], " ")
	if s1 < 0 || s2 < 0 {
		return
	}
	s2 += s1 + 1
	return line[:s1], line[s1+1 : s2], line[s2+1:], true
}

// HTTP2 parses the frame header of the first frame to detect whether the
// connection is an HTTP2 connection.
func HTTP2() Matcher {
	return hasHTTP2Preface
}

func HTTP1HeaderField(name, value string) Matcher {
	return func(r io.Reader) bool {
		return matchHTTP1Field(r, name, func(gotValue string) bool {
			return gotValue == value
		})
	}
}

func HTTP1HeaderFieldPrefix(name, valuePrefix string) Matcher {
	return func(r io.Reader) bool {
		return matchHTTP1Field(r, name, func(gotValue string) bool {
			return strings.HasPrefix(gotValue, valuePrefix)
		})
	}
}

func HTTP2HeaderField(name, value string) Matcher {
	return func(r io.Reader) bool {
		return matchHTTP2Field(ioutil.Discard, r, name, func(gotValue string) bool {
			return gotValue == value
		})
	}
}

func HTTP2HeaderFieldPrefix(name, valuePrefix string) Matcher {
	return func(r io.Reader) bool {
		return matchHTTP2Field(ioutil.Discard, r, name, func(gotValue string) bool {
			return strings.HasPrefix(gotValue, valuePrefix)
		})
	}
}

func HTTP2MatchHeaderFieldSendSettings(name, value string) MatchWriter {
	return func(w io.Writer, r io.Reader) bool {
		return matchHTTP2Field(w, r, name, func(gotValue string) bool {
			return gotValue == value
		})
	}
}

func HTTP2MatchHeaderFieldPrefixSendSettings(name, valuePrefix string) MatchWriter {
	return func(w io.Writer, r io.Reader) bool {
		return matchHTTP2Field(w, r, name, func(gotValue string) bool {
			return strings.HasPrefix(gotValue, valuePrefix)
		})
	}
}

func hasHTTP2Preface(r io.Reader) bool {
	var b [len(http2.ClientPreface)]byte
	last := 0

	for {
		n, err := r.Read(b[last:])
		if err != nil {
			return false
		}

		last += n
		eq := string(b[:last]) == http2.ClientPreface[:last]
		if last == len(http2.ClientPreface) {
			return eq
		}
		if !eq {
			return false
		}
	}
}

func matchHTTP1Field(r io.Reader, name string, matches func(string) bool) (matched bool) {
	req, err := http.ReadRequest(bufio.NewReader(r))
	if err != nil {
		return false
	}

	return matches(req.Header.Get(name))
}

func matchHTTP2Field(w io.Writer, r io.Reader, name string, matches func(string) bool) (matched bool) {
	if !hasHTTP2Preface(r) {
		return false
	}

	done := false
	framer := http2.NewFramer(w, r)
	hdec := hpack.NewDecoder(uint32(4<<10), func(hf hpack.HeaderField) {
		if hf.Name == name {
			done = true
			if matches(hf.Value) {
				matched = true
			}
		}
	})
	for {
		f, err := framer.ReadFrame()
		if err != nil {
			return false
		}

		switch f := f.(type) {
		case *http2.SettingsFrame:
			// Sender acknoweldged the SETTINGS frame. No need to write
			// SETTINGS again.
			if f.IsAck() {
				break
			}
			if err := framer.WriteSettings(); err != nil {
				return false
			}
		case *http2.ContinuationFrame:
			if _, err := hdec.Write(f.HeaderBlockFragment()); err != nil {
				return false
			}
			done = done || f.FrameHeader.Flags&http2.FlagHeadersEndHeaders != 0
		case *http2.HeadersFrame:
			if _, err := hdec.Write(f.HeaderBlockFragment()); err != nil {
				return false
			}
			done = done || f.FrameHeader.Flags&http2.FlagHeadersEndHeaders != 0
		}

		if done {
			return matched
		}
	}
}

type Tree struct {
	root     *ptNode
	maxDepth int // max depth of the tree.
}

func newTree(bs ...[]byte) *Tree {
	max := 0
	for _, b := range bs {
		if max < len(b) {
			max = len(b)
		}
	}
	return &Tree{
		root:     newNode(bs),
		maxDepth: max + 1,
	}
}

func newTreeString(strs ...string) *Tree {
	b := make([][]byte, len(strs))
	for i, s := range strs {
		b[i] = []byte(s)
	}
	return newTree(b...)
}

func (t *Tree) matchPrefix(r io.Reader) bool {
	buf := make([]byte, t.maxDepth)
	n, _ := io.ReadFull(r, buf)
	return t.root.match(buf[:n], true)
}

func (t *Tree) match(r io.Reader) bool {
	buf := make([]byte, t.maxDepth)
	n, _ := io.ReadFull(r, buf)
	return t.root.match(buf[:n], false)
}

type ptNode struct {
	prefix   []byte
	next     map[byte]*ptNode
	terminal bool
}

func newNode(strs [][]byte) *ptNode {
	if len(strs) == 0 {
		return &ptNode{
			prefix:   []byte{},
			terminal: true,
		}
	}

	if len(strs) == 1 {
		return &ptNode{
			prefix:   strs[0],
			terminal: true,
		}
	}

	p, strs := splitPrefix(strs)
	n := &ptNode{
		prefix: p,
	}

	nexts := make(map[byte][][]byte)
	for _, s := range strs {
		if len(s) == 0 {
			n.terminal = true
			continue
		}
		nexts[s[0]] = append(nexts[s[0]], s[1:])
	}

	n.next = make(map[byte]*ptNode)
	for first, rests := range nexts {
		n.next[first] = newNode(rests)
	}

	return n
}

func splitPrefix(bss [][]byte) (prefix []byte, rest [][]byte) {
	if len(bss) == 0 || len(bss[0]) == 0 {
		return prefix, bss
	}

	if len(bss) == 1 {
		return bss[0], [][]byte{{}}
	}

	for i := 0; ; i++ {
		var cur byte
		eq := true
		for j, b := range bss {
			if len(b) <= i {
				eq = false
				break
			}

			if j == 0 {
				cur = b[i]
				continue
			}

			if cur != b[i] {
				eq = false
				break
			}
		}

		if !eq {
			break
		}

		prefix = append(prefix, cur)
	}

	rest = make([][]byte, 0, len(bss))
	for _, b := range bss {
		rest = append(rest, b[len(prefix):])
	}

	return prefix, rest
}

func (n *ptNode) match(b []byte, prefix bool) bool {
	l := len(n.prefix)
	if l > 0 {
		if l > len(b) {
			l = len(b)
		}
		if !bytes.Equal(b[:l], n.prefix) {
			return false
		}
	}

	if n.terminal && (prefix || len(n.prefix) == len(b)) {
		return true
	}

	if l >= len(b) {
		return false
	}

	nextN, ok := n.next[b[l]]
	if !ok {
		return false
	}

	if l == len(b) {
		b = b[l:l]
	} else {
		b = b[l+1:]
	}
	return nextN.match(b, prefix)
}
