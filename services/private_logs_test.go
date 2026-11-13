package services

import (
	"bytes"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// Every private path, refused and let through, in enforce: neither logrus
// nor the access log (which prints the errors of a request) carries a
// tracker or its key.
func TestPrivatePathsLogNoTrackers(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	hook := test.NewGlobal()
	defer hook.Reset()
	defer logrus.SetLevel(logrus.GetLevel())
	logrus.SetLevel(logrus.DebugLevel)
	var access bytes.Buffer // gin.Logger output, as in TestPostResourceMagnetParseErrorHidesTrackers

	_, w, ts, m2t := newPrivateWeb(t, true)
	// gin.Logger takes its writer when the router is built; newPrivateWeb's cleanup restores it.
	gin.DefaultWriter = &access
	r := newRouter()
	r.Use(w.errorHandler)
	w.routes(r)

	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	tokA := signed(t, jwt.MapClaims{"uid": uidA})
	magnet := []byte(privateMagnet(t, trA))
	post := func(tok string, body []byte, want int) {
		t.Helper()
		rec := send(t, r, "POST", "/resource/", tok, body)
		require.Equal(t, want, rec.Code, rec.Body.String())
	}

	// A private .torrent whose announce is trA, refused, then stored.
	upload, err := oneTierTorrent(priv, []string{trA})
	require.NoError(t, err)
	uploadPush := mock.MatchedBy(func(in *tsp.PushRequest) bool { return bytes.Equal(in.Torrent, upload) })
	ts.On("Push", mock.Anything, uploadPush, mock.Anything).Return(nil, infoErr(codes.PermissionDenied, "torrent-store", "private_no_tracker")).Once()
	ts.On("Push", mock.Anything, uploadPush, mock.Anything).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()
	post(tokA, upload, 422)
	post(tokA, upload, 200)

	// An unknown private magnet with tr=trA, anonymous: resolved and pushed, refused.
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs("", true)), mock.Anything).Return(nil, status.Error(codes.NotFound, "not found")).Once()
	m2t.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).
		Return(&m2tp.Magnet2TorrentReply{Torrent: torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}})}, nil).Once()
	ts.On("Push", mock.Anything, pushBy(""), mock.Anything).Return(nil, infoErr(codes.PermissionDenied, "torrent-store", "private_needs_account")).Once()
	post("", magnet, 422)

	// The same magnet, now a known hash, as uidA without a record: the
	// tracker refuses the key, then the check is unavailable, then it
	// accepts and the record is pushed. The refusal above is not cached: the
	// first request loads the hash, the others read it from the cache.
	mockLoader(ts, ih, resolvedPrivate(t))
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).Return(touchRefused(reasonNoRecord)()).Times(3)
	m2t.On("Verify", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.VerifyReply{Outcome: "refused", Class: "key"}, nil).Once()
	post(tokA, magnet, 422)
	m2t.On("Verify", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.Unimplemented, "unknown method Verify")).Once()
	post(tokA, magnet, 503)
	m2t.On("Verify", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.VerifyReply{Outcome: "accepted"}, nil).Once()
	ts.On("Push", mock.Anything, pushBy(uidA), mock.Anything).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()
	post(tokA, magnet, 200)

	// Export of the hash as uidA, allowed.
	yes := true
	ts.On("Files", mock.Anything, mock.Anything, mock.Anything).
		Return(&tsp.FilesReply{Name: "probe", Files: []*tsp.FileInfo{{Path: []string{"probe"}, Length: 1024}}, Private: &yes}, nil).Once()
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).Return(&tsp.TouchReply{Access: "allowed"}, nil).Once()
	rec := send(t, r, "GET", "/resource/"+ih+"/export/0", tokA, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	ts.AssertExpectations(t)
	m2t.AssertExpectations(t)
	require.NotEmpty(t, hook.AllEntries(), "the refusals must still be logged")
	require.NotZero(t, access.Len(), "the access log must still be written")
	for _, e := range hook.AllEntries() {
		s, err := e.String()
		require.NoError(t, err)
		assert.NotContains(t, s, key)
		assert.NotContains(t, s, "tracker.example")
	}
	assert.NotContains(t, access.String(), key)
}
