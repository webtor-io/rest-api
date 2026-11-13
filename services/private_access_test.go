package services

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tsp "github.com/webtor-io/torrent-store/proto"
)

var accessModes = []struct {
	name    string
	enforce bool
}{{"observe", false}, {"enforce", true}}

// cachePrivate puts a one-file torrent under h into the manifest cache with the given flag.
func cachePrivate(t *testing.T, w *Web, h string, private bool) {
	t.Helper()
	res := &Resource{ID: h, Name: "Sintel", Type: ResourceTypeSha1, Private: private,
		Files: []*File{{Path: []string{"Sintel", "Sintel.mp4"}, Size: 1000}}}
	_, err := w.rm.manifests.Get(h, func() (*Resource, error) { return res, nil })
	require.NoError(t, err)
}

func touchRefused(reason string) func() (*tsp.TouchReply, error) {
	return func() (*tsp.TouchReply, error) {
		return nil, infoErr(codes.PermissionDenied, "torrent-store", reason)
	}
}

func touchOK(r *tsp.TouchReply) func() (*tsp.TouchReply, error) {
	return func() (*tsp.TouchReply, error) { return r, nil }
}

// touchOf matches a Touch of h whose Caller passes caller.
func touchOf(h string, caller func(*tsp.Caller) bool) any {
	return mock.MatchedBy(func(in *tsp.TouchRequest) bool { return in.InfoHash == h && caller(in.Caller) })
}

// mockLoader answers the loader's internal Touch and Pull of h with torrent.
func mockLoader(ts *TorrentStoreClientMock, h string, torrent []byte) {
	ts.On("Touch", mock.Anything, touchOf(h, callerIs("", true)), mock.Anything).Return(&tsp.TouchReply{Access: "allowed"}, nil).Once()
	ts.On("Pull", mock.Anything, mock.MatchedBy(func(in *tsp.PullRequest) bool {
		return in.InfoHash == h && callerIs("", true)(in.Caller)
	}), mock.Anything).Return(&tsp.PullReply{Torrent: torrent}, nil).Once()
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var e ErrorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &e), rec.Body.String())
	return e
}

func TestPrivateAccessMatrix(t *testing.T) {
	allowed := &tsp.TouchReply{Access: "allowed"}
	for _, mode := range accessModes {
		// Manifest cached by cachePrivate, so the only Touch is the access check.
		for _, route := range []struct{ name, target string }{
			{routeResource, "/resource/" + tokenTestHash},
			{routeList, "/resource/" + tokenTestHash + "/list"},
		} {
			for _, tc := range []struct {
				name        string
				token       string
				query       bool // token also in ?token=
				touchCaller func(*tsp.Caller) bool
				touch       func() (*tsp.TouchReply, error)
				enforceCode int
				code, why   string
				touches     int
			}{
				{"anonymous", signed(t, jwt.MapClaims{}), false, callerIs("", false), touchRefused("sign_in"), 403, "private_torrent", "sign_in", 1},
				// no X-Token at all (THP's privacy lookup): body reason sign_in, counter label no_token (Р11)
				{"no token", "", false, callerIs("", false), touchRefused("sign_in"), 403, "private_torrent", "sign_in", 1},
				{"uid without record", signed(t, jwt.MapClaims{"uid": uidB}), false, callerIs(uidB, false), touchRefused("no_record"), 403, "private_torrent", "no_record", 1},
				{"uid with record", signed(t, jwt.MapClaims{"uid": uidA}), false, callerIs(uidA, false), touchOK(allowed), 200, "", "", 1},
				{"internal", signed(t, jwt.MapClaims{"internal": true}), false, callerIs("", true), touchOK(allowed), 200, "", "", 1},
				{"?token=", signed(t, jwt.MapClaims{"uid": uidA}), true, nil, nil, 403, "private_torrent", "token_in_query", 0},
			} {
				t.Run(mode.name+"/"+route.name+"/"+tc.name, func(t *testing.T) {
					target := route.target
					if tc.query {
						target += "?token=" + url.QueryEscape(tc.token)
					}
					refusedLabel := tc.why
					if tc.name == "no token" {
						refusedLabel = "no_token"
					}

					h, w, ts, _ := newPrivateWeb(t, mode.enforce)
					cachePrivate(t, w, tokenTestHash, true)
					if tc.touch != nil {
						ts.On("Touch", mock.Anything, touchOf(tokenTestHash, tc.touchCaller), mock.Anything).Return(tc.touch())
					}
					var refused, signIn float64
					if tc.code != "" {
						refused = counter(t, privateRefusedTotal.WithLabelValues(route.name, refusedLabel))
						signIn = counter(t, privateRefusedTotal.WithLabelValues(route.name, reasonSignIn))
					}

					rec := send(t, h, "GET", target, tc.token, nil)

					want := 200
					if mode.enforce {
						want = tc.enforceCode
					}
					require.Equal(t, want, rec.Code, rec.Body.String())
					if want != 200 {
						e := errorBody(t, rec)
						assert.Equal(t, tc.code, e.Code)
						assert.Equal(t, tc.why, e.Reason)
					} else if route.name == routeResource {
						assert.Contains(t, rec.Body.String(), `"private":true`)
					}
					if tc.code != "" {
						assert.Equal(t, 1.0, counter(t, privateRefusedTotal.WithLabelValues(route.name, refusedLabel))-refused)
						if tc.name == "no token" {
							assert.Equal(t, 0.0, counter(t, privateRefusedTotal.WithLabelValues(route.name, reasonSignIn))-signIn)
						}
					}
					ts.AssertNumberOfCalls(t, "Touch", tc.touches)

					// The same hash public: as before, no new request.
					h, w, ts, _ = newPrivateWeb(t, mode.enforce)
					cachePrivate(t, w, tokenTestHash, false)
					// answered, so that asking shows up as a call, not as a mock panic's 500
					ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(allowed, nil).Maybe()
					rec = send(t, h, "GET", target, tc.token, nil)
					require.Equal(t, 200, rec.Code, rec.Body.String())
					if route.name == routeResource {
						assert.Contains(t, rec.Body.String(), `"private":false`)
					}
					ts.AssertNumberOfCalls(t, "Touch", 0)
				})
			}
		}
	}

	// GET /resource/<h>.torrent: internal only, the store is not asked.
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	for _, mode := range accessModes {
		for _, tc := range []struct {
			name        string
			token       string
			enforceCode int
		}{
			{"uid", signed(t, jwt.MapClaims{"uid": uidA}), 403},
			{"internal", signed(t, jwt.MapClaims{"internal": true}), 200},
		} {
			t.Run(mode.name+"/torrent/"+tc.name, func(t *testing.T) {
				h, _, ts, _ := newPrivateWeb(t, mode.enforce)
				mockLoader(ts, ih, priv)
				refused := privateRefusedTotal.WithLabelValues(routeTorrent, codePrivateTorrentFile)
				before := counter(t, refused)

				rec := send(t, h, "GET", "/resource/"+ih+".torrent", tc.token, nil)

				want := 200
				if mode.enforce {
					want = tc.enforceCode
				}
				require.Equal(t, want, rec.Code, rec.Body.String())
				if want == 200 {
					assert.Equal(t, "application/x-bittorrent", rec.Header().Get("Content-Type"))
					assert.NotContains(t, rec.Body.String(), "tracker.invalid")
				} else {
					assert.Equal(t, codePrivateTorrentFile, errorBody(t, rec).Code)
				}
				wantRefused := 0.0
				if tc.enforceCode == 403 {
					wantRefused = 1
				}
				assert.Equal(t, wantRefused, counter(t, refused)-before)
				ts.AssertExpectations(t) // the loader's Touch, once: no other
			})
		}
	}
}

// POST of a stored private hash as sha1 body and as a bare magnet goes through the access rule (route post).
func TestPrivateAccessPost(t *testing.T) {
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	allowed := &tsp.TouchReply{Access: "allowed"}
	for _, mode := range accessModes {
		for _, body := range []struct{ name, b string }{{"sha1", ih}, {"bare magnet", privateMagnet(t)}} {
			for _, tc := range []struct {
				name        string
				token       string
				uid         string
				touch       func() (*tsp.TouchReply, error)
				enforceCode int
			}{
				{"anonymous", signed(t, jwt.MapClaims{}), "", touchRefused("sign_in"), 403},
				{"uid with record", signed(t, jwt.MapClaims{"uid": uidA}), uidA, touchOK(allowed), 200},
			} {
				t.Run(mode.name+"/"+body.name+"/"+tc.name, func(t *testing.T) {
					h, _, ts, _ := newPrivateWeb(t, mode.enforce)
					mockLoader(ts, ih, priv)
					ts.On("Touch", mock.Anything, touchOf(ih, callerIs(tc.uid, false)), mock.Anything).Return(tc.touch()).Once()
					refused := privateRefusedTotal.WithLabelValues(routePost, reasonSignIn)
					before := counter(t, refused)

					rec := send(t, h, "POST", "/resource/", tc.token, []byte(body.b))

					want := 200
					if mode.enforce {
						want = tc.enforceCode
					}
					require.Equal(t, want, rec.Code, rec.Body.String())
					if want == 200 {
						assert.Contains(t, rec.Body.String(), `"private":true`)
					} else {
						e := errorBody(t, rec)
						assert.Equal(t, codePrivateTorrent, e.Code)
						assert.Equal(t, reasonSignIn, e.Reason)
					}
					wantRefused := 0.0
					if tc.enforceCode == 403 {
						wantRefused = 1
					}
					assert.Equal(t, wantRefused, counter(t, refused)-before)
					ts.AssertExpectations(t)
				})
			}
		}
	}
}

// Review Focus 4, S15: no allowance is remembered between requests.
func TestAccessTouchPerRequest(t *testing.T) {
	h, w, ts, _ := newPrivateWeb(t, true)
	cachePrivate(t, w, tokenTestHash, true)
	allowed := &tsp.TouchReply{Access: "allowed"}
	// The record of A is deleted between A's two requests.
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(allowed, nil).Once()
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(touchRefused("no_record")()).Once()
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs("", true)), mock.Anything).Return(allowed, nil).Once()
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs("", false)), mock.Anything).Return(touchRefused("sign_in")()).Once()
	target := "/resource/" + tokenTestHash
	tokA := signed(t, jwt.MapClaims{"uid": uidA})

	rec := send(t, h, "GET", target, tokA, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	rec = send(t, h, "GET", target, tokA, nil)
	require.Equal(t, 403, rec.Code, rec.Body.String())
	assert.Equal(t, reasonNoRecord, errorBody(t, rec).Reason)

	// internal's allowance is not handed to the next caller either
	rec = send(t, h, "GET", target, signed(t, jwt.MapClaims{"internal": true}), nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	rec = send(t, h, "GET", target, "", nil)
	require.Equal(t, 403, rec.Code, rec.Body.String())
	assert.Equal(t, reasonSignIn, errorBody(t, rec).Reason)

	ts.AssertExpectations(t)
}

func TestAccessStoreAnswers(t *testing.T) {
	for _, mode := range accessModes {
		for _, tc := range []struct {
			name                     string
			reply                    *tsp.TouchReply
			err                      error
			observeCode, enforceCode int
			code, msg, outcome       string
		}{
			{"allowed", &tsp.TouchReply{Access: "allowed"}, nil, 200, 200, "", "", "allowed"},
			{"no record", nil, infoErr(codes.PermissionDenied, "torrent-store", "no_record"), 200, 403, "private_torrent", textPrivateTorrent, "refused"},
			{"old store", &tsp.TouchReply{}, nil, 503, 503, "store_outdated", "torrent store is outdated", "outdated"},
			{"store down", nil, status.Error(codes.Unavailable, "s3"), 503, 503, "", "torrent store unavailable", "unavailable"},
			{"disabled", nil, infoErr(codes.FailedPrecondition, "torrent-store", "private_unavailable"), 200, 403, "private_unavailable", "", "disabled"},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				h, w, ts, _ := newPrivateWeb(t, mode.enforce)
				cachePrivate(t, w, tokenTestHash, true)
				ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(tc.reply, tc.err)
				lookups := privateLookupsTotal.WithLabelValues(tc.outcome)
				want := tc.observeCode
				if mode.enforce {
					want = tc.enforceCode
				}
				for i := 1; i <= 2; i++ { // each request asks the store again
					before := counter(t, lookups)

					rec := send(t, h, "GET", "/resource/"+tokenTestHash, signed(t, jwt.MapClaims{"uid": uidA}), nil)

					require.Equal(t, want, rec.Code, rec.Body.String())
					if want != 200 {
						e := errorBody(t, rec)
						assert.Equal(t, tc.code, e.Code)
						if tc.msg != "" {
							assert.Equal(t, tc.msg, e.Error)
						}
					}
					assert.Equal(t, 1.0, counter(t, lookups)-before)
					ts.AssertNumberOfCalls(t, "Touch", i)
				}
			})
		}
	}
}

// Export's links are served to a uid only: its check never passes internal on (Р8).
func TestPrivateAllowedExportCaller(t *testing.T) {
	_, w, ts, _ := newPrivateWeb(t, true)
	ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidA, false)), mock.Anything).Return(&tsp.TouchReply{Access: "allowed"}, nil).Once()
	g, _ := gin.CreateTestContext(httptest.NewRecorder())
	g.Request = httptest.NewRequest("GET", "/resource/"+tokenTestHash+"/export/0", nil)
	g.Request.Header.Set("X-Token", signed(t, jwt.MapClaims{"uid": uidA, "internal": true}))

	_, err := w.privateAllowed(g, &Resource{ID: tokenTestHash, Private: true}, routeExport)

	require.NoError(t, err)
	ts.AssertExpectations(t)
}
