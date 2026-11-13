package services

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// magnetResolvedLabels lists the label pairs of every
// restapi_magnet_resolved_total series the metrics port would serve
// (common-services Prom, promhttp.Handler: the default registry).
func magnetResolvedLabels(t *testing.T) []string {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	var labels []string
	for _, mf := range mfs {
		if mf.GetName() == "restapi_magnet_resolved_total" {
			for _, m := range mf.GetMetric() {
				for _, l := range m.GetLabel() {
					labels = append(labels, l.GetName()+"="+l.GetValue())
				}
			}
		}
	}
	return labels
}

// Both series exist before the first resolution: increase() over a series
// that appears with a 1 reads it as 0, so a lazily created
// private="true" would hide the first private resolution after every
// restart. Declared ahead of TestMagnetResolvedTotal, which runs after it and
// creates both series by resolving; run alone, nothing else here resolves a
// magnet first.
func TestMagnetResolvedSeriesExistUpfront(t *testing.T) {
	assert.ElementsMatch(t, []string{"private=true", "private=false"}, magnetResolvedLabels(t))
}

// Every magnet magnet2torrent resolves for an infohash torrent-store does not
// have is counted once, by the BEP 27 private flag of the info dict it got
// back. A magnet for a stored torrent is not a resolution, and neither is a
// failed one.
func TestMagnetResolvedTotal(t *testing.T) {
	priv, pub := privateTorrent(t), loadSintel(t)
	notFound := status.Error(codes.NotFound, "not found")
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name                    string
		torrent                 []byte
		stored                  bool
		m2tErr                  error
		wantPrivate, wantPublic float64
	}{
		{"private resolved", priv, false, nil, 1, 0},
		{"public resolved", pub, false, nil, 0, 1},
		{"already stored", pub, true, nil, 0, 0},
		{"resolution failed", pub, false, status.Error(codes.Aborted, "fetching torrent takes too long"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			tscl, _ := rm.ts.Get()
			m2tcl, _ := rm.m2t.Get()
			ts, m2t := tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
			if tc.stored {
				ts.On("Touch", any3...).Return(nil, nil)
				ts.On("Pull", any3...).Return(&tsp.PullReply{Torrent: tc.torrent}, nil)
			} else {
				ts.On("Touch", any3...).Return(nil, notFound)
				if tc.m2tErr != nil {
					m2t.On("Magnet2Torrent", any3...).Return(nil, tc.m2tErr)
				} else {
					m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: tc.torrent}, nil)
					// What is pushed is not this test's business.
					ts.On("Push", any3...).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Maybe()
				}
			}
			privC, pubC := magnetResolvedTotal.WithLabelValues("true"), magnetResolvedTotal.WithLabelValues("false")
			beforePriv, beforePub := counter(t, privC), counter(t, pubC)

			// No tr=, so a stored magnet has nothing to push.
			_, _ = rm.Get(context.Background(), []byte("magnet:?xt=urn:btih:"+mustInfoHash(t, tc.torrent)))

			assert.Equal(t, tc.wantPrivate, counter(t, privC)-beforePriv, "magnet_resolved_total{private=true} delta")
			assert.Equal(t, tc.wantPublic, counter(t, pubC)-beforePub, "magnet_resolved_total{private=false} delta")
			ts.AssertExpectations(t)
			m2t.AssertExpectations(t)
		})
	}

	// The series the metrics port serves, under this name.
	assert.ElementsMatch(t, []string{"private=true", "private=false"}, magnetResolvedLabels(t))
}

// A magnet that does not parse is answered 400 with the reason, but neither
// the answer nor any log line carries its text: url.Parse quotes the whole
// URI into its error, tr= passkeys included, and that error used to be
// logged and answered as is.
func TestPostResourceMagnetParseErrorHidesTrackers(t *testing.T) {
	const passkey = "0123456789abcdef0123456789abcdef"
	tr := url.QueryEscape("http://tracker.invalid/announce?passkey=" + passkey)
	base := "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&tr=" + tr
	for _, tc := range []struct{ name, body, reason string }{
		// A pasted magnet with a trailing newline.
		{"control character", base + "\n", "invalid control character"},
		{"bad fragment escape", base + "#%zz", "invalid URL escape"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := test.NewGlobal()
			defer hook.Reset()
			// gin.Logger takes its writer when the router is built.
			var access bytes.Buffer
			prev := gin.DefaultWriter
			defer func() { gin.DefaultWriter = prev }()
			gin.DefaultWriter = &access
			gin.SetMode(gin.TestMode)

			// No mock is set: reaching torrent-store or magnet2torrent fails the case.
			w := &Web{rm: NewTestResourceMap()}
			r := newRouter()
			r.Use(w.errorHandler)
			r.POST("/resource/", w.postResource)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resource/", strings.NewReader(tc.body)))

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "failed to parse magnet")
			assert.Contains(t, rec.Body.String(), tc.reason)
			require.NotEmpty(t, hook.AllEntries(), "the error must still be logged")
			require.NotZero(t, access.Len(), "the access log must still be written")
			logs := access.String()
			for _, e := range hook.AllEntries() {
				s, err := e.String()
				require.NoError(t, err)
				logs += s
			}
			for _, out := range []string{rec.Body.String(), logs} {
				assert.NotContains(t, out, passkey)
				assert.NotContains(t, out, "tracker.invalid")
			}
		})
	}
}
