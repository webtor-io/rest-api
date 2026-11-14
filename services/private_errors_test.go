package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/webtor-io/lazymap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
)

// serveErr answers err through errorHandler, behind the production middleware.
func serveErr(err error) *httptest.ResponseRecorder {
	w := &Web{}
	r := newRouter()
	r.Use(w.errorHandler)
	r.POST("/resource/", func(c *gin.Context) { _ = c.Error(err) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resource/", nil))
	return rec
}

// A typed refusal is answered with its own status, code and reason, ahead of
// the keyword match, with Retry-After when it asks for one. It is logged at
// warn below 500 and at error from 500.
func TestErrorHandlerAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
		retryAfter string
		level      logrus.Level
	}{
		{"private torrent", &apiError{Status: 403, Code: codePrivateTorrent, Reason: reasonNoRecord, Msg: textPrivateTorrent}, 403,
			`{"error":"private torrent: only a signed-in user who added it with their own tracker can play it","code":"private_torrent","reason":"no_record"}`, "", logrus.WarnLevel},
		// Wrapped in every keyword errorHandler searches for: still its own status.
		{"wrapped in keywords", errors.Wrap(&apiError{Status: 422, Code: codePrivateNoTracker, Msg: "x"}, "failed to parse: forbidden: not found: timeout"), 422,
			`{"error":"x","code":"private_no_tracker"}`, "", logrus.WarnLevel},
		{"retry after", &apiError{Status: 503, Code: codePrivateVerifyUnavailable, Msg: "y", RetryAfter: 60}, 503,
			`{"error":"y","code":"private_verify_unavailable"}`, "60", logrus.ErrorLevel},
		{"no code", &apiError{Status: 429, Msg: "z", RetryAfter: 60}, 429, `{"error":"z"}`, "60", logrus.WarnLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := test.NewGlobal()
			defer hook.Reset()
			rec := serveErr(tc.err)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.JSONEq(t, tc.wantBody, rec.Body.String())
			assert.Equal(t, tc.retryAfter, rec.Header().Get("Retry-After"))
			require.Len(t, hook.AllEntries(), 1)
			assert.Equal(t, tc.level, hook.LastEntry().Level)
		})
	}
}

// private is in every resource answer, false included: clients must never
// read its absence as false. verified appears only when set.
func TestResourceResponsePrivateAlwaysPresent(t *testing.T) {
	b, err := json.Marshal(ResourceResponse{ID: "x"})
	require.NoError(t, err)
	assert.Contains(t, string(b), `"private":false`)
	assert.NotContains(t, string(b), "verified")
}

// infoErr is a refusal as torrent-store and magnet2torrent send it.
func infoErr(c codes.Code, domain, reason string) error {
	st, _ := status.New(c, "refused").WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	return st.Err()
}

// privateMagnet is a magnet for privateTorrent(t)'s infohash with the given tr= values (none: bare).
func privateMagnet(t *testing.T, trs ...string) string {
	t.Helper()
	m := "magnet:?xt=urn:btih:" + mustInfoHash(t, privateTorrent(t))
	for _, tr := range trs {
		m += "&tr=" + url.QueryEscape(tr)
	}
	return m
}

func TestFromStoreReasons(t *testing.T) {
	for _, tc := range []struct {
		code              codes.Code
		reason            string
		upload            bool
		status            int
		wantCode, wantWhy string
	}{
		{codes.PermissionDenied, "private_needs_account", true, 422, "private_needs_account", ""},
		{codes.InvalidArgument, "private_no_tracker", true, 422, "private_no_tracker", ""},
		{codes.InvalidArgument, "private_too_many_trackers", true, 422, "private_too_many_trackers", ""},
		{codes.FailedPrecondition, "private_unavailable", true, 422, "private_unavailable", ""},
		{codes.FailedPrecondition, "private_unavailable", false, 403, "private_unavailable", ""},
		{codes.PermissionDenied, "sign_in", false, 403, "private_torrent", "sign_in"},
		{codes.PermissionDenied, "no_record", false, 403, "private_torrent", "no_record"},
	} {
		var ae *apiError
		require.True(t, errors.As(fromStore(infoErr(tc.code, "torrent-store", tc.reason), tc.upload), &ae), tc.reason)
		assert.Equal(t, tc.status, ae.Status, tc.reason)
		assert.Equal(t, tc.wantCode, ae.Code, tc.reason)
		assert.Equal(t, tc.wantWhy, ae.Reason, tc.reason)
	}
	// m2t refusal is the rest-api#6 sentinel.
	assert.ErrorIs(t, fromStore(infoErr(codes.FailedPrecondition, "magnet2torrent", "private_magnet"), true), errPrivateMagnet)
}

// A store status without ErrorInfo is answered as today (a ban is 403 with no
// code, a broken torrent 500); one with ErrorInfo gets its code and reason,
// whatever keyword its text carries.
func TestFromStoreThroughErrorHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	noRecord, err := status.New(codes.PermissionDenied, "record not found").
		WithDetails(&errdetails.ErrorInfo{Domain: "torrent-store", Reason: reasonNoRecord})
	require.NoError(t, err)
	for _, tc := range []struct {
		name              string
		err               error
		wantStatus        int
		wantCode, wantWhy string
	}{
		{"ban", status.Error(codes.PermissionDenied, "banned"), 403, "", ""},
		{"no_record", noRecord.Err(), 403, codePrivateTorrent, reasonNoRecord},
		{"bad torrent", status.Error(codes.FailedPrecondition, "bad torrent"), 500, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveErr(fromStore(tc.err, false))
			assert.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			var body ErrorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.wantCode, body.Code)
			assert.Equal(t, tc.wantWhy, body.Reason)
		})
	}
}

// magnet2torrent refuses a bare private magnet itself. That refusal is
// rest-api#6's own: remembered under the infohash, nothing pushed, 422.
func TestM2TPrivateMagnetRefusal(t *testing.T) {
	ih := mustInfoHash(t, privateTorrent(t))
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	refusing := func() (*ResourceMap, *TorrentStoreClientMock) {
		rm := NewTestResourceMap()
		ts, m2t := resourceMapMocks(rm)
		ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
		ts.On("Push", any3...).Return(nil, nil)
		m2t.On("Magnet2Torrent", any3...).Return(nil, infoErr(codes.FailedPrecondition, "magnet2torrent", "private_magnet"))
		return rm, ts
	}

	rm, ts := refusing()
	_, err := rm.Get(context.Background(), []byte(privateMagnet(t)))
	assert.ErrorIs(t, err, errPrivateMagnet)
	st, _ := rm.privateRefusals.Status(ih)
	assert.Equal(t, lazymap.Done, st) // remembered like rest-api#6's own refusal
	ts.AssertNotCalled(t, "Push", any3...)

	// A fresh map, so the answer is the translated refusal, not the memory of it.
	rm, _ = refusing()
	rec := postResource(t, rm, []byte(privateMagnet(t)))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.JSONEq(t, privateMagnetBody, rec.Body.String())
}

// Every store call of the loaders translates a refusal with ErrorInfo; the
// upload flag tells them apart: private_unavailable is 403 on a read and 422
// on a Push.
func TestLoaderRefusalsTranslated(t *testing.T) {
	sintelID := mustInfoHash(t, loadSintel(t))
	magnet := []byte("magnet:?xt=urn:btih:" + sintelID)
	unavailable := infoErr(codes.FailedPrecondition, "torrent-store", "private_unavailable")
	notFound := status.Error(codes.NotFound, "not found")
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	get := func(b []byte) func(rm *ResourceMap) error {
		return func(rm *ResourceMap) error { _, err := rm.Get(context.Background(), b); return err }
	}
	manifest := func(rm *ResourceMap) error { _, err := rm.GetManifest(context.Background(), sintelID); return err }
	for _, tc := range []struct {
		name  string
		setup func(ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock)
		load  func(rm *ResourceMap) error
		want  int
	}{
		{"Touch", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, unavailable)
		}, get([]byte(sintelID)), 403},
		// ErrorInfo wins over the code: a refusal is never read as an unknown hash.
		{"Touch, refusal sent as NotFound", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, infoErr(codes.NotFound, "torrent-store", "private_unavailable"))
		}, get([]byte(sintelID)), 403},
		{"Pull of a sha1", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, nil)
			ts.On("Pull", any3...).Return(nil, unavailable)
		}, get([]byte(sintelID)), 403},
		{"Pull of a stored magnet", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, nil)
			ts.On("Pull", any3...).Return(nil, unavailable)
		}, get(magnet), 403},
		{"Push of a .torrent", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
			ts.On("Push", any3...).Return(nil, unavailable)
		}, get(loadSintel(t)), 422},
		{"Push of a resolved magnet", func(ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: loadSintel(t)}, nil)
			ts.On("Push", any3...).Return(nil, unavailable)
		}, get(magnet), 422},
		{"Files", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Files", any3...).Return(nil, unavailable)
		}, manifest, 403},
		{"Files, refusal sent as NotFound", func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Files", any3...).Return(nil, infoErr(codes.NotFound, "torrent-store", "private_unavailable"))
		}, manifest, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			tc.setup(resourceMapMocks(rm))
			var ae *apiError
			require.True(t, errors.As(tc.load(rm), &ae))
			assert.Equal(t, tc.want, ae.Status)
			assert.Equal(t, codePrivateUnavailable, ae.Code)
		})
	}
}
