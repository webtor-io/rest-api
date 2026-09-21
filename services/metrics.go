package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const metricsNamespace = "restapi"

// latencyBuckets span from a manifest served out of the lazymap (ms) to a
// magnet resolution that waits on DHT (minutes; anything past 60 s lands in
// +Inf, which is enough — the interesting question there is "how many").
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

var (
	httpRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "HTTP requests by route template, method and response status.",
	}, []string{"route", "method", "status"})

	httpRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency by route template and method.",
		Buckets:   latencyBuckets,
	}, []string{"route", "method"})

	httpRequestsInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: "http",
		Name:      "requests_in_flight",
		Help:      "HTTP requests currently being handled.",
	})

	panicsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Name:      "panics_total",
		Help:      "Handler panics recovered by the router, by route template.",
	}, []string{"route"})

	upstreamRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "upstream",
		Name:      "requests_total",
		Help:      "Calls to upstream services by service, method and outcome.",
	}, []string{"upstream", "method", "outcome"})

	upstreamRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: metricsNamespace,
		Subsystem: "upstream",
		Name:      "request_duration_seconds",
		Help:      "Upstream call latency by service and method.",
		Buckets:   latencyBuckets,
	}, []string{"upstream", "method"})
)

// Upstream names as they appear in the "upstream" label.
const (
	upstreamTorrentStore     = "torrent-store"
	upstreamMagnet2Torrent   = "magnet2torrent"
	upstreamTorrentHTTPProxy = "torrent-http-proxy"
)

// Outcome values. A bounded set: the label must never carry an error text.
const (
	outcomeOK        = "ok"
	outcomeNotFound  = "not_found"
	outcomeForbidden = "forbidden"
	outcomeTimeout   = "timeout"
	outcomeCanceled  = "canceled"
	outcomeError     = "error"
)

// routeLabel is the matched route template, so /resource/<hash>/list from a
// million different hashes is one series. Gin leaves FullPath empty when
// nothing matched (404/405); those get a fixed name rather than the raw path,
// which scanners would otherwise turn into unbounded cardinality.
func routeLabel(c *gin.Context) string {
	if p := c.FullPath(); p != "" {
		return p
	}
	return "unmatched"
}

// methodLabel bounds the method label to the verbs the router can answer;
// anything else a client sends (PROPFIND, arbitrary bytes) is folded into
// "other" so it cannot mint series.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "other"
}

// httpMetrics records request count, latency and concurrency. It must be
// registered outside (before) the recovery middleware: recovery writes the
// 500 inside its own recover(), so a middleware nested inside it would be
// unwound by the panic before any status existed and would either miss the
// request or record it as a 200. Sitting outside, c.Next() returns normally
// for a panicked request with the status recovery set.
func httpMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		httpRequestsInFlight.Inc()
		defer httpRequestsInFlight.Dec()
		c.Next()
		route, method := routeLabel(c), methodLabel(c.Request.Method)
		httpRequestsTotal.WithLabelValues(route, method, strconv.Itoa(c.Writer.Status())).Inc()
		httpRequestDuration.WithLabelValues(route, method).Observe(time.Since(start).Seconds())
	}
}

func observeUpstream(upstream, method, outcome string, start time.Time) {
	upstreamRequestsTotal.WithLabelValues(upstream, method, outcome).Inc()
	upstreamRequestDuration.WithLabelValues(upstream, method).Observe(time.Since(start).Seconds())
}

// upstreamUnaryInterceptor observes every unary RPC on a client connection,
// so a new RPC added to a store call site is counted without anyone having
// to remember to wrap it.
func upstreamUnaryInterceptor(upstream string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		observeUpstream(upstream, rpcName(method), grpcOutcome(err), start)
		return err
	}
}

// rpcName reduces "/proto.TorrentStore/Files" to "Files": the service part
// is already the upstream label.
func rpcName(fullMethod string) string {
	if i := strings.LastIndexByte(fullMethod, '/'); i >= 0 {
		return fullMethod[i+1:]
	}
	return fullMethod
}

// grpcOutcome maps a gRPC call error to an outcome. Context errors are
// checked as well as codes: a deadline that fires before the call leaves the
// client can surface as a bare context error rather than a status.
func grpcOutcome(err error) string {
	if err == nil {
		return outcomeOK
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return outcomeTimeout
	}
	if errors.Is(err, context.Canceled) {
		return outcomeCanceled
	}
	switch status.Code(err) {
	case codes.NotFound:
		return outcomeNotFound
	case codes.PermissionDenied:
		return outcomeForbidden
	case codes.DeadlineExceeded:
		return outcomeTimeout
	case codes.Canceled:
		return outcomeCanceled
	}
	return outcomeError
}

// httpOutcome maps a plain HTTP client call to an outcome. A transport
// error is a timeout only if the deadline or the dialer said so; a refused
// connection is an error. Status classes follow the gRPC mapping so the two
// kinds of upstream can be read on one panel.
func httpOutcome(res *http.Response, err error) string {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return outcomeTimeout
		}
		if errors.Is(err, context.Canceled) {
			return outcomeCanceled
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return outcomeTimeout
		}
		return outcomeError
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return outcomeOK
	case res.StatusCode == http.StatusNotFound:
		return outcomeNotFound
	case res.StatusCode == http.StatusForbidden:
		return outcomeForbidden
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusGatewayTimeout:
		return outcomeTimeout
	}
	return outcomeError
}
