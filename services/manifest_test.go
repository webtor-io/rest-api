package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tsp "github.com/webtor-io/torrent-store/proto"
)

const manifestHash = "08ada5a7a6183aae1e09d831df6748d566095a10"

func TestGetManifest(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Files", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.FilesReply{
		Name: "Sintel",
		Files: []*tsp.FileInfo{
			{Path: []string{"Sintel", "Sintel.de.srt"}, Length: 1652},
			{Path: []string{"Sintel", "Sintel.mp4"}, Length: 129241752},
		},
	}, nil)

	r, err := rm.GetManifest(context.Background(), manifestHash)
	assert.Nil(err)
	assert.Equal(manifestHash, r.ID)
	assert.Equal("Sintel", r.Name)
	assert.Equal(2, len(r.Files))
	assert.Equal([]string{"Sintel", "Sintel.de.srt"}, r.Files[0].Path)
	assert.EqualValues(1652, r.Files[0].Size)
	// Listing must work straight off the manifest resource.
	cr, err := rm.parse([]byte(manifestHash))
	assert.Nil(err)
	assert.Equal(ResourceTypeSha1, cr.Type)
}

func TestGetManifestNotFound(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Files", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, status.Error(codes.NotFound, "unable to find torrent"))

	_, err := rm.GetManifest(context.Background(), manifestHash)
	assert.NotNil(err)
	// Message must contain "not found" so the web error handler maps it to 404.
	assert.Contains(err.Error(), "not found")
}

func TestGetManifestForbidden(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	tsclmm.On("Files", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, status.Error(codes.PermissionDenied, "restricted by the rightholder"))

	_, err := rm.GetManifest(context.Background(), manifestHash)
	assert.NotNil(err)
	// "forbidden" maps to 403 in the web error handler.
	assert.Contains(err.Error(), "forbidden")
}

func TestGetManifestUnimplementedFallsBackToPull(t *testing.T) {
	assert := assert.New(t)
	rm := NewTestResourceMap()
	tsclm, _ := rm.ts.Get()
	tsclmm := tsclm.(*TorrentStoreClientMock)
	// Old torrent-store without the Files RPC → fall back to Touch+Pull.
	tsclmm.On("Files", mock.Anything, mock.Anything, mock.Anything).
		Return(nil, status.Error(codes.Unimplemented, "method Files not implemented"))
	tsclmm.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	tsclmm.On("Pull", mock.Anything, mock.Anything, mock.Anything).
		Return(&tsp.PullReply{Torrent: loadSintel(t)}, nil)

	r, err := rm.GetManifest(context.Background(), manifestHash)
	assert.Nil(err)
	assert.Equal(manifestHash, r.ID)
	assert.Equal("Sintel", r.Name)
	assert.Equal(11, len(r.Files))
	tsclmm.AssertCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
}
