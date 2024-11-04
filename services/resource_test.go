package services

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/magnet2torrent"
	tsp "github.com/webtor-io/torrent-store/proto"
)

func NewTestResourceMap() *ResourceMap {
	ts := NewTorrentStoreMock()
	m2t := NewMagnet2TorrentMock()
	rm := NewResourceMap(ts, m2t)
	return rm
}

var sintel []byte

var sintelMagnet = "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&tr=wss%3A%2F%2Ftracker.fastcast.nz&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F"

func loadSintel(t *testing.T) []byte {
	if sintel != nil {
		return sintel
	}
	require := require.New(t)
	f, err := os.Open("./testdata/Sintel.torrent")
	require.Nil(err)
	defer f.Close()
	s, err := io.ReadAll(f)
	require.Nil(err)
	sintel = s
	return s
}
func assertSintel(t *testing.T, r *Resource) {
	assert := assert.New(t)
	assert.Equal("08ada5a7a6183aae1e09d831df6748d566095a10", r.ID)
	assert.Equal(ResourceTypeTorrent, r.Type)
	assert.Equal("Sintel", r.Name)
	assert.EqualValues(129368064, r.Size)
	assert.Equal(11, len(r.Files))
	assert.Equal([]string{"Sintel.de.srt"}, r.Files[0].Path)
	assert.EqualValues(1652, r.Files[0].Size)
}

func TestResourceMap_parseWithMagnet(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	r, err := rm.parse([]byte(sintelMagnet))
	assert.Nil(err)
	assert.Equal("08ada5a7a6183aae1e09d831df6748d566095a10", r.ID)
	assert.Equal(ResourceTypeMagnet, r.Type)
	assert.Equal("", r.Name)
	assert.EqualValues(0, r.Size)
}

func TestResourceMap_parseWithNonMagnet(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	magnet := "magnet:?xt=urn:btih:da5a7a6183aae1e09d831df6748d566095a10"
	_, err := rm.parse([]byte(magnet))
	assert.NotNil(err)
	assert.ErrorContains(err, "failed to parse magnet")
}

func TestResourceMap_parseWithSha1(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	s := "08ada5a7a6183aae1e09d831df6748d566095a10"
	r, err := rm.parse([]byte(s))
	assert.Nil(err)
	assert.Equal("08ada5a7a6183aae1e09d831df6748d566095a10", r.ID)
	assert.Equal(ResourceTypeSha1, r.Type)
	assert.Equal("", r.Name)
	assert.EqualValues(0, r.Size)
}

func TestResourceMap_parseWithJunkData(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	s := "Junk"
	_, err := rm.parse([]byte(s))
	assert.NotNil(err)
	assert.ErrorContains(err, "failed to parse torrent")
}

func TestResourceMap_parseWithTorrent(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	r, err := rm.parse(loadSintel(t))
	assert.Nil(err)
	assertSintel(t, r)
}

func TestResourceMap_getSha1NotFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclm.(*TorrentStoreClientMock).On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	r, err := rm.Get(context.Background(), []byte("08ada5a7a6183aae1e09d831df6748d566095a10"))
	assert.Nil(r)
	assert.NotNil(err)
	assert.ErrorContains(err, "not found")
	tsclm.(*TorrentStoreClientMock).AssertExpectations(t)
}

func TestResourceMap_getSha1Forbidden(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclm.(*TorrentStoreClientMock).On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.PermissionDenied, "forbidden"))
	r, err := rm.Get(context.Background(), []byte("08ada5a7a6183aae1e09d831df6748d566095a10"))
	assert.Nil(r)
	assert.NotNil(err)
	assert.ErrorContains(err, "forbidden")
	tsclm.(*TorrentStoreClientMock).AssertExpectations(t)
}

func TestResourceMap_getSha1Found(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	tsclmm.On("Pull", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PullReply{
		Torrent: loadSintel(t),
	}, nil)
	r, err := rm.Get(context.Background(), []byte("08ada5a7a6183aae1e09d831df6748d566095a10"))
	assert.Nil(err)
	assertSintel(t, r)
	tsclmm.AssertExpectations(t)
}

func TestResourceMap_torrentFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	r, err := rm.Get(context.Background(), loadSintel(t))
	assertSintel(t, r)
	assert.Nil(err)
	tsclmm.AssertExpectations(t)
}

func TestResourceMap_torrentNotFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	tsclmm.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	r, err := rm.Get(context.Background(), loadSintel(t))
	assertSintel(t, r)
	assert.Nil(err)
	tsclmm.AssertExpectations(t)
}

func TestResourceMap_magnetFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	tsclmm.On("Pull", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PullReply{
		Torrent: loadSintel(t),
	}, nil)
	r, err := rm.Get(context.Background(), []byte(sintelMagnet))
	assertSintel(t, r)
	assert.Nil(err)
	tsclmm.AssertExpectations(t)
}

func TestResourceMap_magnetNotFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	m2tclm, _ := rm.m2t.Get()
	m2tclmm := m2tclm.(*Magnet2TorrentClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	m2tclmm.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.Magnet2TorrentReply{
		Torrent: loadSintel(t),
	}, nil)
	r, err := rm.Get(context.Background(), []byte(sintelMagnet))
	assertSintel(t, r)
	assert.Nil(err)
	tsclmm.AssertExpectations(t)
	m2tclmm.AssertExpectations(t)
}

func TestResourceMap_magnetTimeout(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	m2tclm, _ := rm.m2t.Get()
	m2tclmm := m2tclm.(*Magnet2TorrentClientMock)
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	m2tclmm.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.Magnet2TorrentReply{
		Torrent: loadSintel(t),
	}, nil)
	m2tclmm.sleep = 5 * time.Millisecond
	rm.magnetTimeout = 1 * time.Millisecond
	r, err := rm.Get(context.Background(), []byte(sintelMagnet))
	assert.Nil(r)
	assert.NotNil(err)
	assert.ErrorContains(err, "magnet timeout")
	tsclmm.AssertExpectations(t)
	m2tclmm.AssertExpectations(t)
}
