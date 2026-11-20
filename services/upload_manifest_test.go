package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	uploadKind0  = "9c5db5752b7c0123456789abcdef0123456789ab"
	uploadKind1  = "9c5db5752b7c1123456789abcdef0123456789ab"
	uploadAPIKey = "upload-test-key"
	// kind and source are read past: the reader takes the fields it knows.
	uploadManifestJSON = `{"kind":"upload","source":"x","name":"N","uids":["` + uidA + `"],"reserved":30,` +
		`"files":[{"path":"N/a.mp4","size":10},{"path":"N/d/b.mkv","size":20}]}`
)

// fakeTHP is THP in front of the seeder's index: hits, the last path and query.
type fakeTHP struct {
	*httptest.Server
	hits  atomic.Int32
	mu    sync.Mutex
	path  string
	query url.Values
}

func newFakeTHP(t *testing.T, h http.HandlerFunc) *fakeTHP {
	t.Helper()
	discardLog(t)
	f := &fakeTHP{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.path, f.query = r.URL.Path, r.URL.Query()
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// discardLog keeps the warnings a test provokes out of its output; hooks still get them.
func discardLog(t *testing.T) {
	prev := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prev) })
}

func (f *fakeTHP) last() (string, url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.path, f.query
}

func thpAnswer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeTHP) manifest(prefix string) *UploadManifest {
	return &UploadManifest{target: f.URL + prefix, apiKey: uploadAPIKey, secret: tokenTestSecret, cl: f.Client(), timeout: time.Second}
}

// uploadWeb is newPrivateWeb with the manifest of a fake THP answering status and body.
func uploadWeb(t *testing.T, enforce bool, status int, body string) (http.Handler, *Web, *TorrentStoreClientMock, *fakeTHP) {
	t.Helper()
	f := newFakeTHP(t, thpAnswer(status, body))
	h, w, ts, _ := newPrivateWeb(t, enforce)
	w.rm.uploads = f.manifest("/")
	return h, w, ts, f
}

// jsonOf decodes a response body into a map, so a missing key shows.
func jsonOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m), rec.Body.String())
	return m
}

func int64p(n int64) *int64 { return &n }

func TestUploadManifestRequest(t *testing.T) {
	for _, prefix := range []string{"/", "/torrent-http-proxy/"} {
		t.Run(prefix, func(t *testing.T) {
			f := newFakeTHP(t, thpAnswer(200, uploadManifestJSON))
			before := time.Now().Unix()
			_, err := f.manifest(prefix).Manifest(context.Background(), uploadKind0)
			after := time.Now().Unix()
			require.NoError(t, err)
			path, q := f.last()
			assert.Equal(t, prefix+uploadKind0+"/", path)
			assert.Equal(t, []string{uploadAPIKey}, q["api-key"])
			claims, err := parseToken(q.Get("token"), tokenTestSecret)
			require.NoError(t, err)
			assert.Len(t, claims, 3, "%v", claims)
			assert.Equal(t, uploadKind0, claims["hash"])
			assert.Equal(t, true, claims["internal"])
			exp, _ := claims["exp"].(float64)
			assert.True(t, before+60 <= int64(exp) && int64(exp) <= after+60, "exp %v, now %v..%v", exp, before, after)
		})
	}
}

func TestUploadManifestParse(t *testing.T) {
	files := []*File{{Path: []string{"N", "a.mp4"}, Size: 10}, {Path: []string{"N", "d", "b.mkv"}, Size: 20}}
	for _, c := range []struct {
		id      string
		private bool
	}{{uploadKind0, true}, {uploadKind1, false}} {
		f := newFakeTHP(t, thpAnswer(200, uploadManifestJSON))
		r, err := f.manifest("/").Manifest(context.Background(), c.id)
		require.NoError(t, err)
		assert.Equal(t, &Resource{ID: c.id, Name: "N", Type: ResourceTypeSha1, Private: c.private,
			UIDs: []string{uidA}, Reserved: int64p(30), Files: files}, r)
	}
	// A list from Vault (the directory is gone): no reserved, the files' sum stands for it.
	f := newFakeTHP(t, thpAnswer(200, `{"uids":["`+uidA+`"],"name":"N","files":[{"path":"N/a","size":10},{"path":"N/b","size":5}]}`))
	r, err := f.manifest("/").Manifest(context.Background(), uploadKind0)
	require.NoError(t, err)
	require.NotNil(t, r.Reserved)
	assert.Equal(t, int64(15), *r.Reserved)
}

// Only THP's 404 is "not found"; every other failure is a 503, never a 404.
func TestUploadManifestFailClosed(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			thpAnswer(200, uploadManifestJSON)(w, r)
		case <-r.Context().Done():
		}
	}
	for _, c := range []struct {
		name string
		h    http.HandlerFunc
		prep func(t *testing.T, f *fakeTHP, m *UploadManifest)
		code int
	}{
		{"404", thpAnswer(404, "not found"), nil, 404},
		{"500", thpAnswer(500, ""), nil, 503},
		{"403", thpAnswer(403, "wrong api key"), nil, 503},
		{"429", thpAnswer(429, ""), nil, 503},
		{"502", thpAnswer(502, ""), nil, 503},
		{"not json", thpAnswer(200, "not json"), nil, 503},
		{"slow", slow, func(_ *testing.T, _ *fakeTHP, m *UploadManifest) { m.timeout = 50 * time.Millisecond }, 503},
		{"closed", thpAnswer(200, uploadManifestJSON), func(_ *testing.T, f *fakeTHP, _ *UploadManifest) { f.Close() }, 503},
		{"body over the limit", thpAnswer(200, uploadManifestJSON), func(t *testing.T, _ *fakeTHP, _ *UploadManifest) {
			prev := uploadManifestMaxBody
			uploadManifestMaxBody = 16
			t.Cleanup(func() { uploadManifestMaxBody = prev })
		}, 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeTHP(t, c.h)
			m := f.manifest("/")
			if c.prep != nil {
				c.prep(t, f, m)
			}
			_, err := m.Manifest(context.Background(), uploadKind1)
			require.Error(t, err)
			if c.code == 404 {
				assert.Contains(t, err.Error(), "not found")
				assert.False(t, errors.Is(err, errUploadManifestUnavailable))
			} else {
				assert.True(t, errors.Is(err, errUploadManifestUnavailable), "%v", err)
			}

			h, w, ts, _ := newPrivateWeb(t, false)
			w.rm.uploads = m
			rec := send(t, h, "GET", "/resource/"+uploadKind1, "", nil)
			require.Equal(t, c.code, rec.Code, rec.Body.String())
			if c.code == 404 {
				assert.Equal(t, ErrorResponse{Error: "not found upload=" + uploadKind1}, errorBody(t, rec))
			} else {
				assert.Equal(t, ErrorResponse{Error: "upload manifest unavailable"}, errorBody(t, rec))
			}
			ts.AssertNumberOfCalls(t, "Files", 0)
		})
	}
}

// An upload grows while it is listed: every request asks the seeder.
func TestUploadManifestNoCache(t *testing.T) {
	h, _, ts, f := uploadWeb(t, false, 200, uploadManifestJSON)
	for range 2 {
		rec := send(t, h, "GET", "/resource/"+uploadKind1, "", nil)
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	assert.Equal(t, int32(2), f.hits.Load())
	ts.AssertNumberOfCalls(t, "Files", 0)
	ts.AssertNumberOfCalls(t, "Touch", 0)
}

// No proxy target: an upload is a 503, never a lookup in the store.
func TestUploadManifestNoTarget(t *testing.T) {
	h, _, ts, _ := newPrivateWeb(t, false)
	rec := send(t, h, "GET", "/resource/"+uploadKind0, "", nil)
	require.Equal(t, 503, rec.Code, rec.Body.String())
	assert.Equal(t, ErrorResponse{Error: "upload manifest unavailable"}, errorBody(t, rec))
	ts.AssertNumberOfCalls(t, "Files", 0)
	ts.AssertNumberOfCalls(t, "Touch", 0)
}

func TestUploadResourceReserved(t *testing.T) {
	h, w, _, _ := uploadWeb(t, false, 200, uploadManifestJSON)
	rec := send(t, h, "GET", "/resource/"+uploadKind1, "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	body := jsonOf(t, rec)
	assert.Equal(t, 30.0, body["reserved"])
	assert.Equal(t, 30.0, body["size"])
	assert.Equal(t, 2.0, body["files_count"])
	assert.Equal(t, true, body["multi_file"])
	assert.Equal(t, false, body["private"])

	cachePrivate(t, w, tokenTestHash, false)
	rec = send(t, h, "GET", "/resource/"+tokenTestHash, "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.NotContains(t, jsonOf(t, rec), "reserved")
}

// Review Focus 2: an upload with no file done yet and no part.
func TestUploadEmptyResource(t *testing.T) {
	h, _, _, _ := uploadWeb(t, false, 200, `{"kind":"upload","name":"","uids":["`+uidA+`"],"reserved":0,"files":[]}`)
	rec := send(t, h, "GET", "/resource/"+uploadKind1, "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	body := jsonOf(t, rec)
	assert.Contains(t, body, "reserved")
	assert.Equal(t, 0.0, body["reserved"])
	assert.Equal(t, true, body["multi_file"])
	assert.Equal(t, 0.0, body["size"])
	assert.Equal(t, 0.0, body["files_count"])
	assert.NotContains(t, body, "name")

	rec = send(t, h, "GET", "/resource/"+uploadKind1+"/list", "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Equal(t, 0.0, jsonOf(t, rec)["items_count"])
}

// Review Focus 3: the request URL carries the token and the API key; neither
// reaches the response or the log.
func TestUploadManifestErrorsHideToken(t *testing.T) {
	for _, c := range []struct {
		name string
		prep func(f *fakeTHP)
	}{
		{"403", func(*fakeTHP) {}},
		{"closed", func(f *fakeTHP) { f.Close() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			hook := test.NewGlobal()
			h, _, _, f := uploadWeb(t, false, 403, "wrong api key")
			c.prep(f)
			rec := send(t, h, "GET", "/resource/"+uploadKind0, "", nil)
			require.Equal(t, 503, rec.Code, rec.Body.String())
			texts := []string{rec.Body.String()}
			for _, e := range hook.AllEntries() {
				texts = append(texts, e.Message)
				for k, v := range e.Data {
					texts = append(texts, k+"="+fmt.Sprint(v))
				}
			}
			require.Greater(t, len(hook.AllEntries()), 0, "the failure is logged")
			for _, s := range texts {
				assert.NotContains(t, s, "token=")
				assert.NotContains(t, s, uploadAPIKey)
			}
		})
	}
}

func TestNewUploadManifestFromFlags(t *testing.T) {
	cl := &http.Client{}
	vars := []string{"EXPORT_DOMAIN", "EXPORT_API_KEY", "EXPORT_API_SECRET", "EXPORT_PATH_PREFIX",
		"USE_INTERNAL_TORRENT_HTTP_PROXY", "TORRENT_HTTP_PROXY_SERVICE_HOST", "TORRENT_HTTP_PROXY_SERVICE_PORT"}
	domain := map[string]string{"EXPORT_DOMAIN": "https://thp.invalid", "EXPORT_API_KEY": "k", "EXPORT_API_SECRET": "s"}
	internal := map[string]string{"USE_INTERNAL_TORRENT_HTTP_PROXY": "true", "TORRENT_HTTP_PROXY_SERVICE_HOST": "127.0.0.1",
		"TORRENT_HTTP_PROXY_SERVICE_PORT": "8080", "EXPORT_PATH_PREFIX": "/torrent-http-proxy/"}
	merge := func(ms ...map[string]string) map[string]string {
		out := map[string]string{}
		for _, m := range ms {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	for _, c := range []struct {
		name   string
		env    map[string]string
		target string // "" = nil
		info   string
	}{
		{"nothing", nil, "", "uploads: off, no proxy target"},
		{"export domain", domain, "https://thp.invalid/", "uploads: on"},
		{"internal proxy", merge(domain, internal), "http://127.0.0.1:8080/torrent-http-proxy/", "uploads: on"},
		{"internal without host, with domain", merge(domain, map[string]string{"USE_INTERNAL_TORRENT_HTTP_PROXY": "true"}), "https://thp.invalid/", "uploads: on"},
		{"internal without host or domain", map[string]string{"USE_INTERNAL_TORRENT_HTTP_PROXY": "true"}, "", "uploads: off, no proxy target"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range vars {
				t.Setenv(k, "")
				_ = os.Unsetenv(k)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			discardLog(t)
			hook := test.NewGlobal()
			var m *UploadManifest
			app := cli.NewApp()
			app.Flags = RegisterExportFlags(RegisterCacheMapFlags(nil))
			app.Action = func(ctx *cli.Context) error {
				m = NewUploadManifest(ctx, cl)
				return nil
			}
			require.NoError(t, app.Run([]string{"rest-api"}))
			var lines []string
			for _, e := range hook.AllEntries() {
				if e.Level == log.InfoLevel && strings.HasPrefix(e.Message, "uploads:") {
					lines = append(lines, e.Message)
				}
			}
			assert.Equal(t, []string{c.info}, lines)
			if c.target == "" {
				assert.Nil(t, m)
				return
			}
			require.NotNil(t, m)
			assert.Equal(t, c.target, m.target)
			assert.Equal(t, "k", m.apiKey)
			assert.Equal(t, "s", m.secret)
			assert.Same(t, cl, m.cl)
			assert.Equal(t, uploadManifestTimeout, m.timeout)
		})
	}
}

// An upload has no swarm, so it has no magnet; a torrent keeps the one
// synthesized from its infohash.
func TestUploadResourceNoMagnet(t *testing.T) {
	h, w, _, _ := uploadWeb(t, true, 200, uploadManifestJSON)
	rec := send(t, h, "GET", "/resource/"+uploadKind1, "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "magnet_uri")

	cachePrivate(t, w, tokenTestHash, false)
	rec = send(t, h, "GET", "/resource/"+tokenTestHash, "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"magnet_uri":"magnet:?xt=urn:btih:`+tokenTestHash)
}

// Review Focus 4: the store path, untouched, answers an upload ID 404 and
// never reaches the seeder.
func TestUploadNoTorrentFile(t *testing.T) {
	h, _, ts, f := uploadWeb(t, true, 200, uploadManifestJSON)
	ts.On("Touch", mock.Anything, touchOf(uploadKind0, callerIs("", true)), mock.Anything).
		Return(nil, status.Error(codes.NotFound, "not found"))

	rec := send(t, h, "GET", "/resource/"+uploadKind0+".torrent", signed(t, jwt.MapClaims{"uid": uidA}), nil)
	assert.Equal(t, 404, rec.Code, rec.Body.String())
	rec = send(t, h, "POST", "/resource/", "", []byte(uploadKind0))
	assert.Equal(t, 404, rec.Code, rec.Body.String())

	assert.Equal(t, int32(0), f.hits.Load())
	ts.AssertExpectations(t)
}
