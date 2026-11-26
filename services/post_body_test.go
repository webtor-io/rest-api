package services

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tsp "github.com/webtor-io/torrent-store/proto"
)

// zeros counts the bytes read from it.
type zeros struct{ n int64 }

func (z *zeros) Read(p []byte) (int, error) { clear(p); z.n += int64(len(p)); return len(p), nil }

// A body over maxResourceBody is refused with 413 without being read past
// the limit or parsed, and not read at all when its declared length is over;
// one of exactly maxResourceBody bytes is still read and parsed (zeros are
// no torrent: 400).
func TestPostResourceBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		size     int64
		declared bool
		code     int
		maxRead  int64
	}{
		{"at the limit", maxResourceBody, true, http.StatusBadRequest, maxResourceBody},
		{"over the limit, chunked", maxResourceBody + 1, false, http.StatusRequestEntityTooLarge, maxResourceBody + 1},
		{"over the limit, declared", maxResourceBody + 1, true, http.StatusRequestEntityTooLarge, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No mock is set: reaching torrent-store fails the case.
			w := &Web{rm: NewTestResourceMap()}
			r := newRouter()
			r.Use(w.errorHandler)
			r.POST("/resource/", w.postResource)
			rec := httptest.NewRecorder()
			z := &zeros{}
			req := httptest.NewRequest(http.MethodPost, "/resource/", io.LimitReader(z, tc.size))
			if tc.declared {
				req.ContentLength = tc.size
			}
			r.ServeHTTP(rec, req)
			assert.Equal(t, tc.code, rec.Code, rec.Body.String())
			assert.LessOrEqual(t, z.n, tc.maxRead, "bytes read")
			if tc.code == http.StatusRequestEntityTooLarge {
				assert.JSONEq(t, `{"error":"request body too large: at most 52428800 bytes"}`, rec.Body.String())
			}
		})
	}
}

// storeAccepting is a torrent-store that does not have the hash and takes
// any push. Hand-written, not the testify mock: the mock formats every
// argument it is called with, a multi-MB .torrent included, which would
// drown the allocations measured below.
type storeAccepting struct{ tsp.TorrentStoreClient }

func (storeAccepting) Touch(context.Context, *tsp.TouchRequest, ...grpc.CallOption) (*tsp.TouchReply, error) {
	return nil, status.Error(codes.NotFound, "not found")
}

func (storeAccepting) Push(context.Context, *tsp.PushRequest, ...grpc.CallOption) (*tsp.PushReply, error) {
	return &tsp.PushReply{}, nil
}

func (s storeAccepting) Get() (tsp.TorrentStoreClient, error) { return s, nil }

// bigPublicTorrent is a public .torrent whose size is its piece hashes, as
// with the multi-MB uploads that took rest-api past its memory limit.
func bigPublicTorrent(t *testing.T, pieces int) []byte {
	t.Helper()
	info := metainfo.Info{Name: "big", PieceLength: 16384, Length: int64(pieces) * 16384, Pieces: make([]byte, pieces*20)}
	mi := metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info), Announce: "http://tracker.invalid/announce"}
	var b bytes.Buffer
	require.NoError(t, mi.Write(&b))
	return b.Bytes()
}

func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// POST /resource/ parses a public .torrent once. It used to parse it in the
// handler and again in GetAs, each parse allocating about three copies of
// the piece hashes, all of it live at once while the request ran.
func TestPostResourceParsesTorrentOnce(t *testing.T) {
	body := bigPublicTorrent(t, 200_000) // 4 MB of piece hashes
	rm := NewResourceMap(storeAccepting{}, NewMagnet2TorrentMock())
	read := allocated(func() { _, _ = io.ReadAll(bytes.NewReader(body)) })
	parse := allocated(func() { _, _ = rm.parse(body) })
	var rec *httptest.ResponseRecorder
	post := allocated(func() { rec = postResource(t, rm, body) })
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	t.Logf("body %d B, read %d B, parse %d B, post %d B", len(body), read, parse, post)
	assert.Less(t, post, read+parse*3/2, "POST allocated as much as two parses")
}
