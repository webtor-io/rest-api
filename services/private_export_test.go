package services

import (
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	tsp "github.com/webtor-io/torrent-store/proto"
)

const exportTarget = "/resource/" + tokenTestHash + "/export/0"

// tokenOf is the token query parameter of URL u.
func tokenOf(t *testing.T, u string) string {
	t.Helper()
	p, err := url.Parse(u)
	require.NoError(t, err)
	tok := p.Query().Get("token")
	require.NotEmpty(t, tok, u)
	return tok
}

func TestExportPrivateResigned(t *testing.T) {
	h, w, ts, _ := newPrivateWeb(t, true)
	cachePrivate(t, w, tokenTestHash, true)
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(&tsp.TouchReply{Access: "allowed"}, nil).Once()
	in := jwt.MapClaims{"uid": uidA, "role": "free", "sessionID": "s", "domain": "webtor.io", "hash": "x",
		"rules": map[string]any{"grace": map[string]any{"token": "g"}}, "exp": time.Now().Add(7 * 24 * time.Hour).Unix()}

	code, body := tokenGet(t, h, exportTarget, signed(t, in), true)

	require.Equal(t, 200, code, body)
	var urls []string
	collectURLs(body, &urls)
	require.NotEmpty(t, urls)
	for _, u := range urls {
		got, err := parseToken(tokenOf(t, u), tokenTestSecret)
		require.NoError(t, err)
		assert.Equal(t, tokenTestHash, got["hash"])
		assert.Equal(t, uidA, got["uid"])
		assert.LessOrEqual(t, int64(got["exp"].(float64)), time.Now().Add(24*time.Hour).Unix()+1)
		for _, k := range []string{"role", "sessionID", "domain", "rules"} {
			assert.EqualValues(t, in[k], got[k], k)
		}
	}
	ts.AssertExpectations(t)
}

// Export links are served to a uid only (Р8): its check never passes
// internal on, although the store allows an internal caller any hash.
func TestExportNeverInternal(t *testing.T) {
	allowed := &tsp.TouchReply{Access: "allowed"}
	internal := func(c *tsp.Caller) bool { return c.GetInternal() }
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims
		uid    string // of the one Touch export may make
		want   int
	}{
		{"internal", jwt.MapClaims{"internal": true}, "", 403},
		{"internal with uid", jwt.MapClaims{"internal": true, "uid": uidA}, uidA, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w, ts, _ := newPrivateWeb(t, true)
			cachePrivate(t, w, tokenTestHash, true)
			ts.On("Touch", mock.Anything, touchOf(tokenTestHash, internal), mock.Anything).Return(allowed, nil).Maybe()
			ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs("", false)), mock.Anything).Return(touchRefused("sign_in")()).Maybe()
			ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(allowed, nil).Maybe()

			rec := send(t, h, "GET", exportTarget, signed(t, tc.claims), nil)

			require.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want == 403 {
				e := errorBody(t, rec)
				assert.Equal(t, codePrivateTorrent, e.Code)
				assert.Equal(t, reasonSignIn, e.Reason)
			}
			ts.AssertNumberOfCalls(t, "Touch", 1)
			ts.AssertCalled(t, "Touch", mock.Anything, touchOf(tokenTestHash, callerIs(tc.uid, false)), mock.Anything)
		})
	}
}

// Observe: a refusal leaves the links as they were; an allowance re-signs them.
func TestExportObserveKeepsTokenOnRefusal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		uid     string
		touch   func() (*tsp.TouchReply, error)
		refused float64
	}{
		{"no record", uidB, touchRefused("no_record"), 1},
		{"allowed", uidA, touchOK(&tsp.TouchReply{Access: "allowed"}), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, w, ts, _ := newPrivateWeb(t, false)
			cachePrivate(t, w, tokenTestHash, true)
			ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(tc.uid, false)), mock.Anything).Return(tc.touch()).Once()
			refused := privateRefusedTotal.WithLabelValues(routeExport, reasonNoRecord)
			before := counter(t, refused)
			tok := signed(t, jwt.MapClaims{"uid": tc.uid, "role": "free"})

			code, body := tokenGet(t, h, exportTarget, tok, true)

			require.Equal(t, 200, code, body)
			var urls []string
			collectURLs(body, &urls)
			require.NotEmpty(t, urls)
			for _, u := range urls {
				got := tokenOf(t, u)
				if tc.refused > 0 {
					assert.Equal(t, tok, got, u)
					continue
				}
				assert.NotEqual(t, tok, got, u)
				claims, err := parseToken(got, tokenTestSecret)
				require.NoError(t, err)
				assert.Equal(t, tokenTestHash, claims["hash"])
			}
			assert.Equal(t, tc.refused, counter(t, refused)-before)
			ts.AssertExpectations(t)
		})
	}
}

// Spec negative control: without the marker check rest-api would re-sign for an old store.
func TestExportOutdatedStoreNoResign(t *testing.T) {
	for _, mode := range accessModes {
		t.Run(mode.name, func(t *testing.T) {
			h, w, ts, _ := newPrivateWeb(t, mode.enforce)
			cachePrivate(t, w, tokenTestHash, true)
			ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(&tsp.TouchReply{}, nil).Once()

			rec := send(t, h, "GET", exportTarget, signed(t, jwt.MapClaims{"uid": uidA}), nil)

			require.Equal(t, 503, rec.Code, rec.Body.String())
			assert.Equal(t, codeStoreOutdated, errorBody(t, rec).Code)
			assert.NotContains(t, rec.Body.String(), `"url"`)
			ts.AssertExpectations(t)
		})
	}
}
