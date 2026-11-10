package services

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/magnet2torrent"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// web-ui recognises this answer by its status; the text is for API callers.
const privateMagnetBody = `{"error":"private torrent: this magnet has no tracker; upload the .torrent file or use a magnet that includes your tracker"}`

func postResource(t *testing.T, rm *ResourceMap, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := &Web{rm: rm}
	r := newRouter()
	r.Use(w.errorHandler)
	r.POST("/resource/", w.postResource)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resource/", bytes.NewReader(body)))
	return rec
}

// resolvedPrivate is what magnet2torrent returns for a private torrent: its
// info dict and no trackers.
func resolvedPrivate(t *testing.T) []byte {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(privateTorrent(t)))
	require.NoError(t, err)
	mi.Announce, mi.AnnounceList, mi.UrlList = "", nil, nil
	var buf bytes.Buffer
	require.NoError(t, mi.Write(&buf))
	return buf.Bytes()
}

// A magnet without tr= that magnet2torrent resolves to a BEP 27 private
// torrent nobody uploaded is refused with 422 and nothing reaches
// torrent-store: stored bare, it would be served with no tracker to every
// later magnet for the hash. The uploader's .torrent is accepted right after.
func TestPostResourceBarePrivateMagnetRefused(t *testing.T) {
	priv := privateTorrent(t)
	magnet := "magnet:?xt=urn:btih:" + mustInfoHash(t, priv) + "&dn=probe"
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}

	rm := NewTestResourceMap()
	tscl, _ := rm.ts.Get()
	m2tcl, _ := rm.m2t.Get()
	ts, m2t := tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
	ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
	m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil).Once()
	// Stubbed, so a push is counted rather than failing as an unexpected call.
	ts.On("Push", any3...).Return(nil, nil)

	rec := postResource(t, rm, []byte(magnet))

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.JSONEq(t, privateMagnetBody, rec.Body.String())
	ts.AssertNumberOfCalls(t, "Push", 0)
	m2t.AssertExpectations(t)

	// The refusal is not cached under the infohash: the .torrent goes through.
	rec = postResource(t, rm, priv)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ts.AssertNumberOfCalls(t, "Push", 1)
}

// A magnet that carries the user's own tr= is accepted for a private torrent
// nobody uploaded: the resolved info dict is pushed with those trackers and
// the answer is 200, stripped of them like any private torrent's.
func TestPostResourcePrivateMagnetWithTrackersPushed(t *testing.T) {
	priv := privateTorrent(t)
	const tr = "http://user.invalid/announce?passkey=0123456789abcdef0123456789abcdef"
	ih := mustInfoHash(t, priv)
	magnet := "magnet:?xt=urn:btih:" + ih + "&tr=" + url.QueryEscape(tr)
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}

	rm := NewTestResourceMap()
	tscl, _ := rm.ts.Get()
	m2tcl, _ := rm.m2t.Get()
	ts, m2t := tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
	ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
	m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil).Once()
	ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
		mi, err := metainfo.Load(bytes.NewReader(in.Torrent))
		return err == nil && mi.Announce == tr && mi.HashInfoBytes().HexString() == ih
	}), mock.Anything).Return(nil, nil).Once()

	rec := postResource(t, rm, []byte(magnet))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "user.invalid")
	ts.AssertExpectations(t)
	m2t.AssertExpectations(t)
}

// A public magnet resolves and is pushed as before: with its tr= trackers
// when it has some, as resolved when it has none.
func TestPostResourcePublicMagnetStillPushed(t *testing.T) {
	pub := loadSintel(t)
	const extra = "http://extra.invalid/announce"
	id := "magnet:?xt=urn:btih:" + mustInfoHash(t, pub)
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name, magnet string
		pushed       func(torrent []byte) bool
	}{
		{"with tr=", id + "&tr=" + url.QueryEscape(extra), func(b []byte) bool { return strings.Contains(string(b), extra) }},
		{"without tr=", id, func(b []byte) bool { return bytes.Equal(b, pub) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			tscl, _ := rm.ts.Get()
			m2tcl, _ := rm.m2t.Get()
			ts, m2t := tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
			ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: pub}, nil)
			ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
				return tc.pushed(in.Torrent)
			}), mock.Anything).Return(nil, nil).Once()

			rec := postResource(t, rm, []byte(tc.magnet))

			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			ts.AssertExpectations(t)
			m2t.AssertExpectations(t)
		})
	}
}

// The 422 is chosen by error identity, ahead of the keyword match: wrapped
// in text that names every keyword, it is still a 422 with the fixed body,
// and the same text without the identity is not a 422. It is an expected
// client condition, so it is logged at warn, not error.
func TestErrorHandlerPrivateMagnetByIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name  string
		err   error
		want  int
		level logrus.Level
	}{
		{"sentinel", errPrivateMagnet, http.StatusUnprocessableEntity, logrus.WarnLevel},
		{"wrapped in keywords", errors.Wrap(errPrivateMagnet, "failed to parse: forbidden: not found: timeout: deadline exceeded"), http.StatusUnprocessableEntity, logrus.WarnLevel},
		{"same text, other error", errors.New(errPrivateMagnet.Error()), http.StatusInternalServerError, logrus.ErrorLevel},
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
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
			if tc.want == http.StatusUnprocessableEntity {
				assert.JSONEq(t, privateMagnetBody, rec.Body.String())
			}
			require.Len(t, hook.AllEntries(), 1)
			assert.Equal(t, tc.level, hook.LastEntry().Level)
		})
	}
}
