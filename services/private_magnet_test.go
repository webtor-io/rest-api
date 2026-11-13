package services

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/webtor-io/lazymap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// web-ui recognises this answer by its status; the text is for API callers.
const privateMagnetBody = `{"error":"private torrent: this magnet has no tracker; upload the .torrent file or use a magnet that includes your tracker","code":"private_magnet"}`

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

	// The refusal is remembered for bare magnets only: the .torrent goes through.
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

func resourceMapMocks(rm *ResourceMap) (*TorrentStoreClientMock, *Magnet2TorrentClientMock) {
	tscl, _ := rm.ts.Get()
	m2tcl, _ := rm.m2t.Get()
	return tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
}

// pushedWith matches a push whose torrent carries tracker.
func pushedWith(tracker string) any {
	return mock.MatchedBy(func(in *tsp.PushRequest) bool {
		return strings.Contains(string(in.Torrent), tracker)
	})
}

// A .torrent or a magnet with a tracker that arrives while a bare magnet for
// the same unknown infohash is being resolved shares that lazymap loader, and
// so its refusal. Neither is refused by get, so each is then run on its own
// and pushed instead of being answered 422.
func TestResourceMapJoinerOfRefusedBareMagnetPushed(t *testing.T) {
	priv := privateTorrent(t)
	bare := "magnet:?xt=urn:btih:" + mustInfoHash(t, priv)
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{".torrent", priv},
		{"magnet with tr=", []byte(bare + "&tr=" + url.QueryEscape(passkeyTracker))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, m2t := resourceMapMocks(rm)
			ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			m2t.On("Magnet2Torrent", any3...).Run(func(mock.Arguments) {
				once.Do(func() { close(started) })
				<-release
			}).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil)
			ts.On("Push", mock.Anything, pushedWith(passkeyTracker), mock.Anything).Return(nil, nil).Once()

			bareErr, joinedErr := make(chan error, 1), make(chan error, 1)
			go func() {
				_, err := rm.Get(context.Background(), []byte(bare))
				bareErr <- err
			}()
			<-started // the bare magnet's loader is running under the infohash
			go func() {
				_, err := rm.Get(context.Background(), tc.body)
				joinedErr <- err
			}()
			// Time to reach the running loader. Too short could only let the
			// joiner start a loader of its own and pass without the fix,
			// never fail with it.
			time.Sleep(100 * time.Millisecond)
			close(release)

			assert.ErrorIs(t, <-bareErr, errPrivateMagnet)
			assert.NoError(t, <-joinedErr)
			ts.AssertExpectations(t)
		})
	}
}

// A bare magnet refused as private is remembered under its infohash: a retry
// is answered 422 without resolving it again. Only bare magnets consult it,
// and only once torrent-store has said it lacks the hash; a push of the hash
// forgets it.
func TestResourceMapPrivateRefusalRemembered(t *testing.T) {
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	bare := []byte("magnet:?xt=urn:btih:" + ih)
	withTr := []byte(string(bare) + "&tr=" + url.QueryEscape(passkeyTracker))
	notFound := status.Error(codes.NotFound, "not found")
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	remembered := func(rm *ResourceMap) bool {
		st, _ := rm.privateRefusals.Status(ih)
		return st == lazymap.Done
	}
	for _, tc := range []struct {
		name string
		// then is the second request, after the bare magnet's refusal.
		then        []byte
		setup       func(ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock)
		wantErr     error
		resolutions int
		forgotten   bool
	}{
		{"bare retry", bare, func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
		}, errPrivateMagnet, 1, false},
		{"magnet with tr= resolves and pushes", withTr, func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
			ts.On("Push", mock.Anything, pushedWith(passkeyTracker), mock.Anything).Return(nil, nil).Once()
		}, nil, 2, true},
		{".torrent pushes", priv, func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
			ts.On("Push", mock.Anything, pushedWith(passkeyTracker), mock.Anything).Return(nil, nil).Once()
		}, nil, 1, true},
		// Uploaded meanwhile, through another replica for all this one knows.
		{"bare retry, stored meanwhile", bare, func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound).Once()
			ts.On("Touch", any3...).Return(nil, nil)
			ts.On("Pull", any3...).Return(&tsp.PullReply{Torrent: priv}, nil)
		}, nil, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, m2t := resourceMapMocks(rm)
			tc.setup(ts, m2t)
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil)

			_, err := rm.Get(context.Background(), bare)
			require.ErrorIs(t, err, errPrivateMagnet)
			require.True(t, remembered(rm), "the refusal must be remembered")

			_, err = rm.Get(context.Background(), tc.then)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
			m2t.AssertNumberOfCalls(t, "Magnet2Torrent", tc.resolutions)
			// One look-up per request: a refused bare magnet is not run twice.
			ts.AssertNumberOfCalls(t, "Touch", 2)
			if tc.forgotten {
				assert.False(t, remembered(rm), "a push of the hash must forget the refusal")
			}
			ts.AssertExpectations(t)
		})
	}
}

// An empty or blank tr= is no tracker. A private magnet with nothing else is
// refused like a bare one rather than stored with an empty announce; a
// public one is pushed as before.
func TestResourceMapBlankTrackerPrivateMagnet(t *testing.T) {
	priv, pub := privateTorrent(t), loadSintel(t)
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name, tr string
		torrent  []byte
		refused  bool
	}{
		{"private, empty", "&tr=", priv, true},
		{"private, space", "&tr=%20", priv, true},
		{"private, blanks", "&tr=+&tr=%09", priv, true},
		{"public, empty", "&tr=", pub, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := pub
			if tc.refused {
				resolved = resolvedPrivate(t)
			}
			rm := NewTestResourceMap()
			ts, m2t := resourceMapMocks(rm)
			ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: resolved}, nil).Once()
			ts.On("Push", any3...).Return(nil, nil)

			_, err := rm.Get(context.Background(), []byte("magnet:?xt=urn:btih:"+mustInfoHash(t, tc.torrent)+tc.tr))

			if tc.refused {
				assert.ErrorIs(t, err, errPrivateMagnet)
				ts.AssertNotCalled(t, "Push", any3...)
			} else {
				assert.NoError(t, err)
				ts.AssertNumberOfCalls(t, "Push", 1)
			}
		})
	}
}
