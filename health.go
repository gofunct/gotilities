package gotilities

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"
)

type ProbeUtil interface {
	MakeProbe(registry prometheus.Registerer, namespace string) Handler
}

type Prober struct{}

// NewMetricsHandler returns a healthcheck Handler that also exposes metrics
// into the provided Prometheus registry.
func (g *Prober) MakeProbe(registry prometheus.Registerer, namespace string) Handler {
	return &metricsHandler{
		handler:   NewHandler(),
		registry:  registry,
		namespace: namespace,
	}
}

type Handler interface {
	http.Handler

	AddLivenessCheck(name string, check Check)

	AddReadinessCheck(name string, check Check)

	LiveEndpoint(http.ResponseWriter, *http.Request)

	ReadyEndpoint(http.ResponseWriter, *http.Request)
}

// Check is a health/readiness check.
type Check func() error

var ErrNoData = errors.New("no data yet")

func Async(check Check, interval time.Duration) Check {
	return AsyncWithContext(context.Background(), check, interval)
}

func AsyncWithContext(ctx context.Context, check Check, interval time.Duration) Check {
	// create a chan that will buffer the most recent check result
	result := make(chan error, 1)

	result <- ErrNoData

	update := func() {
		err := check()
		<-result
		result <- err
	}

	// spawn a background goroutine to run the check
	go func() {

		update()

		// loop forever or until the context is canceled
		ticker := time.Tick(interval)
		for {
			select {
			case <-ticker:
				update()
			case <-ctx.Done():
				return
			}
		}
	}()

	// return a Check function that closes over our result and mutex
	return func() error {
		// peek at the head of the channel, then put it back
		err := <-result
		result <- err
		return err
	}
}

// TCPDialCheck returns a Check that checks TCP connectivity to the provided
// endpoint.
func TCPDialCheck(addr string, timeout time.Duration) Check {
	return func() error {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// HTTPGetCheck returns a Check that performs an HTTP GET request against the
// specified URL. The check fails if the response times out or returns a non-200
// status code.
func HTTPGetCheck(url string, timeout time.Duration) Check {
	client := http.Client{
		Timeout: timeout,
		// never follow redirects
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return func() error {
		resp, err := client.Get(url)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("returned status %d", resp.StatusCode)
		}
		return nil
	}
}

// DatabasePingCheck returns a Check that validates connectivity to a
// database/sql.DB using Ping().
func DatabasePingCheck(database *sql.DB, timeout time.Duration) Check {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if database == nil {
			return fmt.Errorf("database is nil")
		}
		return database.PingContext(ctx)
	}
}

// DNSResolveCheck returns a Check that makes sure the provided host can resolve
// to at least one IP address within the specified timeout.
func DNSResolveCheck(host string, timeout time.Duration) Check {
	resolver := net.Resolver{}
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		addrs, err := resolver.LookupHost(ctx, host)
		if err != nil {
			return err
		}
		if len(addrs) < 1 {
			return fmt.Errorf("could not resolve host")
		}
		return nil
	}
}

// GoroutineCountCheck returns a Check that fails if too many goroutines are
// running (which could indicate a resource leak).
func GoroutineCountCheck(threshold int) Check {
	return func() error {
		count := runtime.NumGoroutine()
		if count > threshold {
			return fmt.Errorf("too many goroutines (%d > %d)", count, threshold)
		}
		return nil
	}
}

// basicHandler is a basic Handler implementation.
type basicHandler struct {
	http.ServeMux
	checksMutex     sync.RWMutex
	livenessChecks  map[string]Check
	readinessChecks map[string]Check
}

// NewHandler creates a new basic Handler
func NewHandler() Handler {
	h := &basicHandler{
		livenessChecks:  make(map[string]Check),
		readinessChecks: make(map[string]Check),
	}
	h.Handle("/live", http.HandlerFunc(h.LiveEndpoint))
	h.Handle("/ready", http.HandlerFunc(h.ReadyEndpoint))
	return h
}

func (s *basicHandler) LiveEndpoint(w http.ResponseWriter, r *http.Request) {
	s.handle(w, r, s.livenessChecks)
}

func (s *basicHandler) ReadyEndpoint(w http.ResponseWriter, r *http.Request) {
	s.handle(w, r, s.readinessChecks, s.livenessChecks)
}

func (s *basicHandler) AddLivenessCheck(name string, check Check) {
	s.checksMutex.Lock()
	defer s.checksMutex.Unlock()
	s.livenessChecks[name] = check
}

func (s *basicHandler) AddReadinessCheck(name string, check Check) {
	s.checksMutex.Lock()
	defer s.checksMutex.Unlock()
	s.readinessChecks[name] = check
}

func (s *basicHandler) collectChecks(checks map[string]Check, resultsOut map[string]string, statusOut *int) {
	s.checksMutex.RLock()
	defer s.checksMutex.RUnlock()
	for name, check := range checks {
		if err := check(); err != nil {
			*statusOut = http.StatusServiceUnavailable
			resultsOut[name] = err.Error()
		} else {
			resultsOut[name] = "OK"
		}
	}
}

func (s *basicHandler) handle(w http.ResponseWriter, r *http.Request, checks ...map[string]Check) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	checkResults := make(map[string]string)
	status := http.StatusOK
	for _, checks := range checks {
		s.collectChecks(checks, checkResults, &status)
	}

	// write out the response code and content type header
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	// unless ?full=1, return an empty body. Kubernetes only cares about the
	// HTTP status code, so we won't waste bytes on the full body.
	if r.URL.Query().Get("full") != "1" {
		w.Write([]byte("{}\n"))
		return
	}

	// otherwise, write the JSON body ignoring any encoding errors (which
	// shouldn't really be possible since we're encoding a map[string]string).
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "    ")
	encoder.Encode(checkResults)
}

type metricsHandler struct {
	handler   Handler
	registry  prometheus.Registerer
	namespace string
}

func (h *metricsHandler) AddLivenessCheck(name string, check Check) {
	h.handler.AddLivenessCheck(name, h.wrap(name, check))
}

func (h *metricsHandler) AddReadinessCheck(name string, check Check) {
	h.handler.AddReadinessCheck(name, h.wrap(name, check))
}

func (h *metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *metricsHandler) LiveEndpoint(w http.ResponseWriter, r *http.Request) {
	h.handler.LiveEndpoint(w, r)
}

func (h *metricsHandler) ReadyEndpoint(w http.ResponseWriter, r *http.Request) {
	h.handler.ReadyEndpoint(w, r)
}

func (h *metricsHandler) wrap(name string, check Check) Check {
	h.registry.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace:   h.namespace,
			Subsystem:   "healthcheck",
			Name:        "status",
			Help:        "Current check status (0 indicates success, 1 indicates failure)",
			ConstLabels: prometheus.Labels{"check": name},
		},
		func() float64 {
			if check() == nil {
				return 0
			}
			return 1
		},
	))
	return check
}

// TimeoutError is the error returned when a Timeout-wrapped Check takes too long
type timeoutError time.Duration

func (e timeoutError) Error() string {
	return fmt.Sprintf("timed out after %s", time.Duration(e).String())
}

// Timeout returns whether this error is a timeout (always true for timeoutError)
func (e timeoutError) Timeout() bool {
	return true
}

// Temporary returns whether this error is temporary (always true for timeoutError)
func (e timeoutError) Temporary() bool {
	return true
}

// Timeout adds a timeout to a Check. If the underlying check takes longer than
// the timeout, it returns an error.
func Timeout(check Check, timeout time.Duration) Check {
	return func() error {
		c := make(chan error, 1)
		go func() { c <- check() }()
		select {
		case err := <-c:
			return err
		case <-time.After(timeout):
			return timeoutError(timeout)
		}
	}
}
