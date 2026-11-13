package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			w := &Web{}
			r := newRouter()
			r.Use(w.errorHandler)
			r.POST("/resource/", func(c *gin.Context) { _ = c.Error(tc.err) })
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resource/", nil))
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
