package services

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/webtor-io/lazymap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// newPrivateWeb is production's router (w.routes) over mocks, configured
// as production is: secret, subdomains, one download node per role.
func newPrivateWeb(t *testing.T, enforce bool) (h http.Handler, w *Web, ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	prevGin, prevLog := gin.DefaultWriter, log.StandardLogger().Out
	gin.DefaultWriter = io.Discard
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		gin.DefaultWriter = prevGin
		log.SetOutput(prevLog)
	})

	ns := tokenNodes(t)
	ub := &URLBuilder{
		sd:                NewSubdomains(ns),
		cm:                newTestCacheMap(&http.Client{Transport: noNetwork{}}, time.Second),
		domain:            tokenTestDomain,
		apiSecret:         tokenTestSecret,
		apiRole:           "free",
		useSubdomains:     true,
		subdomainsK8SPool: "seeder",
		pathPrefix:        "/",
	}
	st := &SpeedTest{nsp: ns, domain: tokenTestDomain, apiSecret: tokenTestSecret, apiRole: "free",
		useSubdomains: true, subdomainsK8SPool: "seeder"}
	rm := NewTestResourceMap()
	ts, m2t = resourceMapMocks(rm)
	list := NewList()
	w = &Web{rm: rm, c: list, st: st, secret: tokenTestSecret, enforce: enforce, vl: newVerifyLimiter(),
		e: NewExport(NewDownloadExporter(ub), NewStreamExporter(ub, NewTagBuilder(ub, list)))}
	r := newRouter()
	r.Use(w.errorHandler)
	w.routes(r)
	return r, w, ts, m2t
}

// send runs one request; token, when set, goes in X-Token.
func send(t *testing.T, h http.Handler, method, target, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("X-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A private .torrent is pushed by the handler with the caller on every
// upload: the resource cache, shared between users, never answers it.
func TestPrivateTorrentUploadPerRequest(t *testing.T) {
	h, _, ts, _ := newPrivateWeb(t, false)
	priv := privateTorrent(t)
	// A loader would look the hash up first: answered, so that path fails on
	// what it pushes rather than on an unexpected call. None is expected.
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found")).Maybe()
	var callers []string
	ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
		return bytes.Equal(in.Torrent, priv) && in.Source == "site" && !in.DryRun && in.Caller != nil
	}), mock.Anything).Run(func(a mock.Arguments) {
		callers = append(callers, a.Get(1).(*tsp.PushRequest).GetCaller().GetUid())
	}).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil)
	for _, uid := range []string{uidA, uidB} { // within the 600 s cache of one ResourceMap
		rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uid}), priv)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"private":true`)
		assert.NotContains(t, rec.Body.String(), "tracker.invalid")
	}
	assert.Equal(t, []string{uidA, uidB}, callers)
	ts.AssertNotCalled(t, "Touch", mock.Anything, mock.Anything, mock.Anything)
}

// What an upload of a private .torrent answers and counts, by the store's reply.
func TestPrivateTorrentUploadAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   *tsp.PushReply
		err     error
		status  int
		code    string
		outcome string
	}{
		// one store refusal stands for all four: the full table of reasons is TestFromStoreReasons
		{"needs account", nil, infoErr(codes.PermissionDenied, "torrent-store", "private_needs_account"), 422, "private_needs_account", "refused"},
		{"stoplist", nil, status.Error(codes.PermissionDenied, "banned"), 403, "", "refused"},
		// the store's geometry check: answered as a public .torrent is, never as retryable
		{"malformed", nil, status.Error(codes.InvalidArgument, "malformed torrent"), 500, "", "refused"},
		{"store down", nil, status.Error(codes.Unavailable, "s3"), 503, "", "failed"},
		{"old store", &tsp.PushReply{}, nil, 503, "store_outdated", "outdated"},
		{"updated", &tsp.PushReply{RecordOutcome: "updated"}, nil, 200, "", "updated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, ts, _ := newPrivateWeb(t, false)
			ts.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(tc.reply, tc.err).Once()
			c := privateUploadsTotal.WithLabelValues(sourceSite, tc.outcome)
			before := counter(t, c)

			rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), privateTorrent(t))

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
			if tc.code == "" {
				assert.NotContains(t, body, "code")
			} else {
				assert.Equal(t, tc.code, body["code"])
			}
			assert.Equal(t, 1.0, counter(t, c)-before)
			ts.AssertExpectations(t)
		})
	}
}

// The upload source comes from the verified token's domain, for the handler's
// Push of a private .torrent and the loader's Push of a public one alike.
func TestRapidAPIUploadSource(t *testing.T) {
	for _, body := range []struct {
		name    string
		torrent []byte
	}{{"private", privateTorrent(t)}, {"public", loadSintel(t)}} {
		for _, tc := range []struct{ domain, source string }{
			{"rapidapi.com", "rapidapi"},
			{"webtor.io", "site"},
		} {
			t.Run(body.name+"/"+tc.domain, func(t *testing.T) {
				h, _, ts, _ := newPrivateWeb(t, false)
				// The public .torrent's loader looks the hash up; the private one is not looked up.
				ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found")).Maybe()
				ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
					return in.Source == tc.source && callerIs(uidA, false)(in.Caller)
				}), mock.Anything).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()

				rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA, "domain": tc.domain}), body.torrent)

				require.Equal(t, 200, rec.Code, rec.Body.String())
				ts.AssertExpectations(t)
			})
		}
	}
}

// Moved from TestResourceMapPrivateRefusalRemembered: an upload forgets a bare magnet's refusal.
func TestPrivateUploadForgetsBareRefusal(t *testing.T) {
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	h, w, ts, m2t := newPrivateWeb(t, false)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	m2t.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil).Once()
	ts.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()
	remembered := func() bool {
		st, _ := w.rm.privateRefusals.Status(ih)
		return st == lazymap.Done
	}

	rec := send(t, h, "POST", "/resource/", "", []byte(privateMagnet(t)))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.True(t, remembered(), "the bare magnet's refusal must be remembered")

	rec = send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), priv)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.False(t, remembered(), "an upload of the hash must forget the refusal")
	ts.AssertExpectations(t)
	m2t.AssertExpectations(t)
}
