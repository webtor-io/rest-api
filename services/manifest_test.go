package services

import (
	"context"
	"strings"
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

func TestMagnetFromInfoHash(t *testing.T) {
	assert := assert.New(t)
	assert.Equal("magnet:?xt=urn:btih:abc", magnetFromInfoHash("abc", ""))
	assert.Equal("magnet:?xt=urn:btih:abc&dn=My+File", magnetFromInfoHash("abc", "My File"))
	// Synthesized demo magnet must keep the demo flag as a prefix so web-ui
	// demo detection (HasPrefix) keeps working.
	demoFlag := "magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10"
	assert.True(strings.HasPrefix(magnetFromInfoHash("08ada5a7a6183aae1e09d831df6748d566095a10", "Sintel"), demoFlag))
}

func TestFillResourceStructureSingleFile(t *testing.T) {
	assert := assert.New(t)
	w := &Web{c: NewList()}
	r := &Resource{ID: "h", Name: "movie.mkv", Files: []*File{{Path: []string{"movie.mkv"}, Size: 100}}}
	rr := &ResourceResponse{ID: r.ID, Name: r.Name}
	w.fillResourceStructure(rr, r)
	assert.False(rr.MultiFile)
	if assert.NotNil(rr.File) {
		assert.Equal("/movie.mkv", rr.File.PathStr)
		assert.Equal(ListTypeFile, rr.File.Type)
	}
}

func TestFillResourceStructureMultiFile(t *testing.T) {
	assert := assert.New(t)
	w := &Web{c: NewList()}
	r := &Resource{ID: "h", Name: "show", Files: []*File{
		{Path: []string{"show", "e01.mkv"}, Size: 100},
		{Path: []string{"show", "e02.mkv"}, Size: 200},
	}}
	rr := &ResourceResponse{}
	w.fillResourceStructure(rr, r)
	assert.True(rr.MultiFile)
	assert.Nil(rr.File)
}

// A torrent with a single file that still sits inside a folder (multi-file
// mode with one entry) must be treated as multi-file, since the file is not
// at the root and web-ui can't address it from the name alone.
func TestFillResourceStructureSingleFileWrapped(t *testing.T) {
	assert := assert.New(t)
	w := &Web{c: NewList()}
	r := &Resource{ID: "h", Name: "wrap", Files: []*File{{Path: []string{"wrap", "only.mkv"}, Size: 100}}}
	rr := &ResourceResponse{}
	w.fillResourceStructure(rr, r)
	assert.True(rr.MultiFile)
	assert.Nil(rr.File)
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
