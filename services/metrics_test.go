package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Metrics live in the default registry, shared by every test in the package,
// so assertions are on deltas, never on absolute values.
func counter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	return testutil.ToFloat64(c)
}

func histogramCount(t *testing.T, o prometheus.Observer) uint64 {
	t.Helper()
	m, ok := o.(prometheus.Metric)
	if !ok {
		t.Fatalf("observer %T is not a Metric", o)
	}
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		t.Fatal(err)
	}
	return d.GetHistogram().GetSampleCount()
}

func TestHTTPMetricsCountsRouteMethodStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	r.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	total := httpRequestsTotal.WithLabelValues("/thing/:id", "GET", "204")
	dur := httpRequestDuration.WithLabelValues("/thing/:id", "GET")
	before, beforeDur := counter(t, total), histogramCount(t, dur)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thing/42", nil))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d", w.Code)
	}
	if got := counter(t, total) - before; got != 1 {
		t.Errorf("requests_total{/thing/:id,GET,204} delta = %v, want 1", got)
	}
	if got := histogramCount(t, dur) - beforeDur; got != 1 {
		t.Errorf("request_duration_seconds{/thing/:id,GET} observations delta = %v, want 1", got)
	}
	// The raw path must not become a label: one series per template.
	if got := counter(t, httpRequestsTotal.WithLabelValues("/thing/42", "GET", "204")); got != 0 {
		t.Errorf("raw path leaked into route label: %v", got)
	}
}

func TestHTTPMetricsUnmatchedRouteIsOneSeries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	r.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	total := httpRequestsTotal.WithLabelValues("unmatched", "GET", "404")
	before := counter(t, total)

	for _, p := range []string{"/nope", "/wp-admin/x", "/thing/1/extra"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	if got := counter(t, total) - before; got != 3 {
		t.Errorf("requests_total{unmatched,GET,404} delta = %v, want 3", got)
	}
}

func TestHTTPMetricsFoldsUnknownMethods(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	r.GET("/thing", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	total := httpRequestsTotal.WithLabelValues("unmatched", "other", "404")
	before := counter(t, total)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/thing", nil))
	if got := counter(t, total) - before; got != 1 {
		t.Errorf("requests_total{unmatched,other,404} delta = %v, want 1", got)
	}
	if got := counter(t, httpRequestsTotal.WithLabelValues("unmatched", "BREW", "404")); got != 0 {
		t.Errorf("raw method leaked into label: %v", got)
	}
}

func TestHTTPMetricsInFlight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()
	var seen float64
	r.GET("/slow", func(c *gin.Context) {
		seen = testutil.ToFloat64(httpRequestsInFlight)
		c.Status(http.StatusNoContent)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))

	if seen != 1 {
		t.Errorf("in_flight during handler = %v, want 1", seen)
	}
	if got := testutil.ToFloat64(httpRequestsInFlight); got != 0 {
		t.Errorf("in_flight after request = %v, want 0", got)
	}
}

// A panicked request is a 500 like any other in the request counter, is
// counted separately as a panic, and does not leak an in-flight slot. This
// depends on the middleware order in newRouter — metrics outside recovery.
func TestHTTPMetricsPanicIsA500AndCounted(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	gin.SetMode(gin.TestMode)
	r := newRouter()
	r.GET("/boom/:id", func(c *gin.Context) { panic("kaboom") })

	total := httpRequestsTotal.WithLabelValues("/boom/:id", "GET", "500")
	panics := panicsTotal.WithLabelValues("/boom/:id")
	before, beforePanics := counter(t, total), counter(t, panics)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom/1", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := counter(t, total) - before; got != 1 {
		t.Errorf("requests_total{/boom/:id,GET,500} delta = %v, want 1", got)
	}
	if got := counter(t, panics) - beforePanics; got != 1 {
		t.Errorf("panics_total{/boom/:id} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(httpRequestsInFlight); got != 0 {
		t.Errorf("in_flight after panic = %v, want 0", got)
	}
}

func TestGRPCOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, outcomeOK},
		{"not found", status.Error(codes.NotFound, "no such hash"), outcomeNotFound},
		{"permission denied", status.Error(codes.PermissionDenied, "banned"), outcomeForbidden},
		{"deadline exceeded", status.Error(codes.DeadlineExceeded, "late"), outcomeTimeout},
		{"canceled", status.Error(codes.Canceled, "gone"), outcomeCanceled},
		{"unavailable", status.Error(codes.Unavailable, "down"), outcomeError},
		{"unimplemented", status.Error(codes.Unimplemented, "old store"), outcomeError},
		{"internal", status.Error(codes.Internal, "boom"), outcomeError},
		{"bare context deadline", context.DeadlineExceeded, outcomeTimeout},
		{"bare context canceled", context.Canceled, outcomeCanceled},
		{"plain error", errors.New("dial failed"), outcomeError},
	} {
		if got := grpcOutcome(tc.err); got != tc.want {
			t.Errorf("%s: grpcOutcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestHTTPOutcome(t *testing.T) {
	resp := func(code int) *http.Response { return &http.Response{StatusCode: code} }
	for _, tc := range []struct {
		name string
		res  *http.Response
		err  error
		want string
	}{
		{"200", resp(200), nil, outcomeOK},
		{"204", resp(204), nil, outcomeOK},
		{"404", resp(404), nil, outcomeNotFound},
		{"403", resp(403), nil, outcomeForbidden},
		{"408", resp(408), nil, outcomeTimeout},
		{"504", resp(504), nil, outcomeTimeout},
		{"500", resp(500), nil, outcomeError},
		{"302", resp(302), nil, outcomeError},
		{"context deadline", nil, context.DeadlineExceeded, outcomeTimeout},
		{"context canceled", nil, context.Canceled, outcomeCanceled},
		{"net timeout", nil, &net.OpError{Op: "dial", Err: timeoutErr{}}, outcomeTimeout},
		{"refused", nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}, outcomeError},
	} {
		if got := httpOutcome(tc.res, tc.err); got != tc.want {
			t.Errorf("%s: httpOutcome = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUpstreamUnaryInterceptor(t *testing.T) {
	ic := upstreamUnaryInterceptor("store-under-test")
	invoke := func(err error) grpc.UnaryInvoker {
		return func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return err }
	}

	okc := upstreamRequestsTotal.WithLabelValues("store-under-test", "Files", outcomeOK)
	nfc := upstreamRequestsTotal.WithLabelValues("store-under-test", "Pull", outcomeNotFound)
	dur := upstreamRequestDuration.WithLabelValues("store-under-test", "Files")
	beforeOK, beforeNF, beforeDur := counter(t, okc), counter(t, nfc), histogramCount(t, dur)

	if err := ic(context.Background(), "/proto.TorrentStore/Files", nil, nil, nil, invoke(nil)); err != nil {
		t.Fatal(err)
	}
	want := status.Error(codes.NotFound, "no")
	if err := ic(context.Background(), "/proto.TorrentStore/Pull", nil, nil, nil, invoke(want)); !errors.Is(err, want) {
		t.Fatalf("interceptor must pass the error through, got %v", err)
	}

	if got := counter(t, okc) - beforeOK; got != 1 {
		t.Errorf("requests_total{Files,ok} delta = %v, want 1", got)
	}
	if got := counter(t, nfc) - beforeNF; got != 1 {
		t.Errorf("requests_total{Pull,not_found} delta = %v, want 1", got)
	}
	if got := histogramCount(t, dur) - beforeDur; got != 1 {
		t.Errorf("request_duration_seconds{Files} observations delta = %v, want 1", got)
	}
	// The full method name must not be the label.
	if got := counter(t, upstreamRequestsTotal.WithLabelValues("store-under-test", "/proto.TorrentStore/Files", outcomeOK)); got != 0 {
		t.Errorf("full method name leaked into label: %v", got)
	}
}
