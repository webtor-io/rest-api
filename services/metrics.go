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

	// How many magnet resolutions turn out to be BEP 27 private torrents. Such
	// a torrent only works with the uploader's .torrent (the passkey is in its
	// trackers, never in the info dict), so this sizes the magnet users that
	// path concerns. It counts magnet2torrent calls that returned an info
	// dict, not requests and not distinct torrents: lazymap shares one loader
	// between concurrent requests for a hash and keeps a success for 10
	// minutes, by which time torrent-store has the hash; a result that was
	// not stored (the push failed) is resolved, and counted, again by the
	// next request for it. That includes every bare private magnet, which is
	// refused and never stored, so private="true" counts attempts, not
	// torrents: retries within 10 minutes of a refusal are answered from
	// ResourceMap.privateRefusals without a resolution and are not counted,
	// later ones are. A .torrent or a magnet with a tracker that shared a
	// refused magnet's loader is run again on its own, and a magnet among
	// them resolves, and counts, a second time.
	magnetResolvedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "magnet",
		Name:      "resolved_total",
		Help:      "Magnets resolved by magnet2torrent for an infohash torrent-store did not have, by the info dict's BEP 27 private flag.",
	}, []string{"private"})

	// Pushes of BEP 27 private torrents with the caller's uid (pushPrivate):
	// an uploaded .torrent, or a magnet with tr= of an unknown hash. outcome
	// is the store's record outcome (another non-empty one is unchanged),
	// refused (ErrorInfo, PermissionDenied, or InvalidArgument without
	// ErrorInfo: a malformed torrent), failed (any other error) or
	// outdated (no outcome: a store that predates private records).
	privateUploadsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "private",
		Name:      "uploads_total",
		Help:      "Private torrent pushes to torrent-store with the caller's uid, by upload source and outcome.",
	}, []string{"source", "outcome"})

	// Refusals of the private access rule (and, from the known-magnet path,
	// of tracker checks), counted in both modes: in observe the request goes
	// on, so this is what enforce would have refused. No mode label (S19):
	// restapi_private_enforce says which mode a time window ran in.
	privateRefusedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "private",
		Name:      "refused_total",
		Help:      "Private torrent requests refused, by route and reason; counted in observe mode too.",
	}, []string{"route", "reason"})

	// torrent-store's answer to the per-request access check of a private hash.
	privateLookupsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricsNamespace,
		Subsystem: "private",
		Name:      "record_lookups_total",
		Help:      "Access checks (Touch with the caller) of private torrents in torrent-store, by outcome.",
	}, []string{"outcome"})

	privateEnforce = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: metricsNamespace,
		Subsystem: "private",
		Name:      "enforce",
		Help:      "1 when private torrent access refusals are enforced, 0 when they are only counted.",
	})
)

func init() {
	// Both series from the start at 0: increase() takes a series' first
	// sample as its baseline, so one created by its first Inc would hide that
	// resolution, and private="true" is rare enough for that to matter.
	for _, private := range []string{"true", "false"} {
		magnetResolvedTotal.WithLabelValues(private)
	}
	for _, source := range []string{sourceSite, sourceRapidAPI, sourceMagnet} {
		for _, outcome := range []string{"inserted", "updated", "unchanged", "refused", "failed", "outdated"} {
			privateUploadsTotal.WithLabelValues(source, outcome)
		}
	}
	for _, route := range []string{routeResource, routeList, routeExport, routeTorrent, routePost} {
		for _, reason := range []string{reasonSignIn, reasonNoRecord, reasonTokenInQuery, codePrivateUnavailable,
			codePrivateTorrentFile, codePrivateNeedsAccount, codePrivateNoTracker, codePrivateTooManyTrackers,
			codePrivateTrackerRefused, codePrivateVerifyUnavailable, reasonRateLimited, reasonNoToken} {
			privateRefusedTotal.WithLabelValues(route, reason)
		}
	}
	for _, outcome := range []string{"allowed", "refused", "unavailable", "disabled", "outdated"} {
		privateLookupsTotal.WithLabelValues(outcome)
	}
}

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
	// A status with ErrorInfo from torrent-store or magnet2torrent: a refusal
	// by rule (sign_in, private_magnet, ...), not a failure of the upstream.
	outcomeRefused = "refused"
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
	if _, ok := errorInfoReason(err); ok {
		return outcomeRefused
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
