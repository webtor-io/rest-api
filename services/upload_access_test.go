package services

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// uploadNoUIDsJSON is uploadManifestJSON with no uid listed.
const uploadNoUIDsJSON = `{"kind":"upload","source":"x","name":"N","uids":[],"reserved":30,` +
	`"files":[{"path":"N/a.mp4","size":10},{"path":"N/d/b.mkv","size":20}]}`

// linkClaims returns the verified claims of every link in an export body.
func linkClaims(t *testing.T, body map[string]any) []jwt.MapClaims {
	t.Helper()
	var urls []string
	collectURLs(body, &urls)
	require.NotEmpty(t, urls)
	var out []jwt.MapClaims
	for _, u := range urls {
		got, err := parseToken(tokenOf(t, u), tokenTestSecret)
		require.NoError(t, err, u)
		out = append(out, got)
	}
	return out
}

// A kind 0 upload opens to a uid of its manifest only.
func TestUploadAccessMatrix(t *testing.T) {
	for _, route := range []struct{ name, target string }{
		{routeResource, "/resource/" + uploadKind0},
		{routeList, "/resource/" + uploadKind0 + "/list"},
	} {
		for _, tc := range []struct {
			name, token        string
			query              bool // token also in ?token=
			code               int
			reason, msg, label string
		}{
			{"owner", signed(t, jwt.MapClaims{"uid": uidA}), false, 200, "", "", ""},
			{"internal", signed(t, jwt.MapClaims{"internal": true}), false, 200, "", "", ""},
			{"no token", "", false, 403, reasonSignIn, textPrivateUpload, reasonNoToken},
			{"no uid", signed(t, jwt.MapClaims{"role": "free"}), false, 403, reasonSignIn, textPrivateUpload, reasonSignIn},
			{"foreign uid", signed(t, jwt.MapClaims{"uid": uidB}), false, 403, reasonNoRecord, textPrivateUpload, reasonNoRecord},
			{"?token=", signed(t, jwt.MapClaims{"uid": uidA}), true, 403, reasonTokenInQuery, textPrivateTorrent, reasonTokenInQuery},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				target := route.target
				if tc.query {
					target += "?token=" + url.QueryEscape(tc.token)
				}
				h, _, ts, _ := uploadWeb(t, 200, uploadManifestJSON)
				var refused float64
				if tc.code == 403 {
					refused = counter(t, privateRefusedTotal.WithLabelValues(route.name, tc.label))
				}

				rec := send(t, h, "GET", target, tc.token, nil)

				require.Equal(t, tc.code, rec.Code, rec.Body.String())
				if tc.code == 403 {
					assert.Equal(t, ErrorResponse{Error: tc.msg, Code: codePrivateTorrent, Reason: tc.reason}, errorBody(t, rec))
					assert.Equal(t, 1.0, counter(t, privateRefusedTotal.WithLabelValues(route.name, tc.label))-refused)
				} else if route.name == routeResource {
					assert.Contains(t, rec.Body.String(), `"private":true`)
				}
				ts.AssertNumberOfCalls(t, "Touch", 0)
				ts.AssertNumberOfCalls(t, "Files", 0)
			})
		}
	}
	t.Run("no uids", func(t *testing.T) {
		h, _, ts, _ := uploadWeb(t, 200, uploadNoUIDsJSON)
		refused := counter(t, privateRefusedTotal.WithLabelValues(routeResource, reasonNoRecord))

		rec := send(t, h, "GET", "/resource/"+uploadKind0, signed(t, jwt.MapClaims{"uid": uidA}), nil)

		require.Equal(t, 403, rec.Code, rec.Body.String())
		assert.Equal(t, ErrorResponse{Error: textPrivateUpload, Code: codePrivateTorrent, Reason: reasonNoRecord}, errorBody(t, rec))
		assert.Equal(t, 1.0, counter(t, privateRefusedTotal.WithLabelValues(routeResource, reasonNoRecord))-refused)
		ts.AssertNumberOfCalls(t, "Touch", 0)
		ts.AssertNumberOfCalls(t, "Files", 0)
	})

	// A private torrent is still decided by the store, never by the seeder.
	t.Run("private torrent", func(t *testing.T) {
		h, w, ts, f := uploadWeb(t, 200, uploadManifestJSON)
		cachePrivate(t, w, tokenTestHash, true)
		ts.On("Touch", mock.Anything, touchOf(tokenTestHash, callerIs(uidB, false)), mock.Anything).Return(touchRefused(reasonNoRecord)()).Once()

		rec := send(t, h, "GET", "/resource/"+tokenTestHash, signed(t, jwt.MapClaims{"uid": uidB}), nil)

		require.Equal(t, 403, rec.Code, rec.Body.String())
		assert.Equal(t, ErrorResponse{Error: textPrivateTorrent, Code: codePrivateTorrent, Reason: reasonNoRecord}, errorBody(t, rec))
		ts.AssertExpectations(t)
		assert.Equal(t, int32(0), f.hits.Load())
	})
}

// Export of a kind 0 upload: the owner's links are re-signed as a private
// torrent's are; internal is no pass on export.
func TestUploadExport(t *testing.T) {
	const target = "/resource/" + uploadKind0 + "/export/0"
	in := jwt.MapClaims{"uid": uidA, "internal": true, "role": "free", "sessionID": "s",
		"rules": map[string]any{"grace": map[string]any{"token": "g"}}, "exp": time.Now().Add(7 * 24 * time.Hour).Unix()}
	t.Run("owner", func(t *testing.T) {
		h, _, ts, _ := uploadWeb(t, 200, uploadManifestJSON)

		code, body := tokenGet(t, h, target, signed(t, in), true)

		require.Equal(t, 200, code, body)
		for _, got := range linkClaims(t, body) {
			assert.Equal(t, uploadKind0, got["hash"])
			assert.Equal(t, uidA, got["uid"])
			assert.LessOrEqual(t, int64(got["exp"].(float64)), time.Now().Add(24*time.Hour).Unix()+1)
			assert.NotContains(t, got, "internal")
			for _, k := range []string{"role", "sessionID", "rules"} {
				assert.EqualValues(t, in[k], got[k], k)
			}
		}
		ts.AssertNumberOfCalls(t, "Touch", 0)
	})
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims
		reason string
	}{
		{"internal", jwt.MapClaims{"internal": true}, reasonSignIn},
		{"foreign", jwt.MapClaims{"uid": uidB}, reasonNoRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, ts, _ := uploadWeb(t, 200, uploadManifestJSON)

			rec := send(t, h, "GET", target, signed(t, tc.claims), nil)

			require.Equal(t, 403, rec.Code, rec.Body.String())
			assert.Equal(t, ErrorResponse{Error: textPrivateUpload, Code: codePrivateTorrent, Reason: tc.reason}, errorBody(t, rec))
			ts.AssertNumberOfCalls(t, "Touch", 0)
		})
	}
}

// An upload ID in upper case is lowered before the manifest and the rule:
// the seeder is asked for the lower-case ID, and the answer carries it.
func TestUploadAccessUpperCaseID(t *testing.T) {
	up := strings.ToUpper(uploadKind0)
	h, _, ts, f := uploadWeb(t, 200, uploadManifestJSON)
	owner := signed(t, jwt.MapClaims{"uid": uidA})

	rec := send(t, h, "GET", "/resource/"+up, signed(t, jwt.MapClaims{"uid": uidB}), nil)
	require.Equal(t, 403, rec.Code, rec.Body.String())
	assert.Equal(t, reasonNoRecord, errorBody(t, rec).Reason)
	path, _ := f.last()
	assert.Equal(t, "/"+uploadKind0+"/", path)

	rec = send(t, h, "GET", "/resource/"+up, owner, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"id":"`+uploadKind0)

	rec = send(t, h, "GET", "/resource/"+up+"/list", owner, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	code, body := tokenGet(t, h, "/resource/"+up+"/export/0", owner, true)
	require.Equal(t, 200, code, body)
	for _, got := range linkClaims(t, body) {
		assert.Equal(t, uploadKind0, got["hash"])
	}
	ts.AssertNumberOfCalls(t, "Touch", 0)
}

// Kind 1 is public: anyone opens it, and export keeps the token it is given.
func TestUploadKind1Public(t *testing.T) {
	h, _, ts, _ := uploadWeb(t, 200, uploadNoUIDsJSON)
	anon := signed(t, jwt.MapClaims{"role": "free"})

	rec := send(t, h, "GET", "/resource/"+uploadKind1, anon, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"private":false`)

	code, body := tokenGet(t, h, "/resource/"+uploadKind1+"/export/0", anon, true)
	require.Equal(t, 200, code, body)
	var urls []string
	collectURLs(body, &urls)
	require.NotEmpty(t, urls)
	for _, u := range urls {
		assert.Equal(t, anon, tokenOf(t, u), u)
	}
	ts.AssertNumberOfCalls(t, "Touch", 0)
}
