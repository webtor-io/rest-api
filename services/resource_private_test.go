package services

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/magnet2torrent"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// Fake passkey on a non-routable host.
const passkeyTracker = "http://tracker.invalid/announce?passkey=AAA"

// privateTorrent is a BEP 27 torrent with the passkey in announce,
// announce-list and url-list.
func privateTorrent(t *testing.T) []byte {
	t.Helper()
	p := true
	ib, err := bencode.Marshal(metainfo.Info{Name: "probe", Length: 1024, PieceLength: 1024, Pieces: make([]byte, 20), Private: &p})
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, (&metainfo.MetaInfo{
		InfoBytes:    ib,
		Announce:     passkeyTracker,
		AnnounceList: metainfo.AnnounceList{{passkeyTracker}},
		UrlList:      metainfo.UrlList{"http://tracker.invalid/seed?passkey=AAA"},
	}).Write(&buf))
	return buf.Bytes()
}

func assertNoTrackers(t *testing.T, r *Resource) {
	t.Helper()
	assert.NotContains(t, string(r.Torrent), "tracker.invalid")
	assert.NotContains(t, r.MagnetURI, "tr=")
	assert.NotContains(t, r.MagnetURI, "ws=")
	mi, err := metainfo.Load(bytes.NewReader(r.Torrent))
	require.NoError(t, err)
	assert.Equal(t, r.ID, mi.HashInfoBytes().HexString())
}

func TestParseTorrentPrivateStripsTrackers(t *testing.T) {
	b := privateTorrent(t)
	r, err := NewTestResourceMap().parseTorrent(b)
	require.NoError(t, err)
	assertNoTrackers(t, r)
	assert.Contains(t, string(b), passkeyTracker, "input must not be mutated")
}

func TestParseTorrentPublicKeepsBytes(t *testing.T) {
	b := loadSintel(t)
	r, err := NewTestResourceMap().parseTorrent(b)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(b, r.Torrent))
	assert.Contains(t, r.MagnetURI, "tr=")
	assert.Contains(t, r.MagnetURI, "ws=")
}

// torrent-store must get the passkey-bearing bytes on every path (it is the
// only place the seeder takes trackers from); only the answer is stripped.
func TestResourceMapPrivatePushesOriginal(t *testing.T) {
	priv := privateTorrent(t)
	id := "magnet:?xt=urn:btih:" + mustInfoHash(t, priv)
	magnet := id + "&tr=" + url.QueryEscape(passkeyTracker)
	notFound := status.Error(codes.NotFound, "not found")
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name  string
		body  []byte
		setup func(ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock)
	}{
		{"uploaded torrent", priv, func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
		}},
		{"stored magnet", []byte(magnet), func(ts *TorrentStoreClientMock, _ *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, nil)
			ts.On("Pull", any3...).Return(&tsp.PullReply{Torrent: priv}, nil)
		}},
		{"resolved magnet", []byte(magnet), func(ts *TorrentStoreClientMock, m2t *Magnet2TorrentClientMock) {
			ts.On("Touch", any3...).Return(nil, notFound)
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: priv}, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			tscl, _ := rm.ts.Get()
			m2tcl, _ := rm.m2t.Get()
			ts, m2t := tscl.(*TorrentStoreClientMock), m2tcl.(*Magnet2TorrentClientMock)
			tc.setup(ts, m2t)
			ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
				return strings.Contains(string(in.Torrent), passkeyTracker)
			}), mock.Anything).Return(nil, nil).Once()
			r, err := rm.Get(context.Background(), tc.body)
			require.NoError(t, err)
			assertNoTrackers(t, r)
			ts.AssertExpectations(t)
			m2t.AssertExpectations(t)
		})
	}
}

func mustInfoHash(t *testing.T, b []byte) string {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(b))
	require.NoError(t, err)
	return mi.HashInfoBytes().HexString()
}
