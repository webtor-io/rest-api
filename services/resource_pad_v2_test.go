package services

import (
	"bytes"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
)

func encodeTorrent(t *testing.T, info any) []byte {
	t.Helper()
	ib, err := bencode.Marshal(info)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, (&metainfo.MetaInfo{InfoBytes: ib}).Write(&buf))
	return buf.Bytes()
}

// A v1 torrent with BEP 47 padding: the flag comes from the "p" attribute
// where the encoder set it, and from the ".pad/<n>" name where it did not.
// Padding stays in Files at its own index.
func TestParseTorrent_MarksPadFiles(t *testing.T) {
	pieces := make([]byte, 20*4)
	info := metainfo.Info{
		Name:        "T",
		PieceLength: 1024,
		Pieces:      pieces,
		Files: []metainfo.FileInfo{
			{Path: []string{"a.mkv"}, Length: 1000},
			{Path: []string{".pad", "24"}, Length: 24, ExtendedFileAttrs: metainfo.ExtendedFileAttrs{Attr: "p"}},
			{Path: []string{"b.mkv"}, Length: 1000},
			{Path: []string{".pad", "24"}, Length: 24}, // no attr: name only
			{Path: []string{"c.srt"}, Length: 10},
		},
	}
	r, err := NewTestResourceMap().parseTorrent(encodeTorrent(t, info))
	require.NoError(t, err)
	require.Len(t, r.Files, 5)
	want := []bool{false, true, false, true, false}
	for i, f := range r.Files {
		require.Equal(t, want[i], f.Pad, "file %d %v", i, f.Path)
	}
	require.Equal(t, []string{"T", "b.mkv"}, r.Files[2].Path)
}

// v2 torrents align files to piece boundaries: the piece range of each file
// follows the metainfo offset, not the running sum of lengths.
func TestParseTorrent_V2PieceRanges(t *testing.T) {
	// The info dict by hand: metainfo.Info does not round-trip a FileTree
	// through bencode.Marshal (a file node is {"": {"length": n}}).
	v1Pieces := make([]byte, 20*4) // v1 piece hashes, as in a hybrid
	info := map[string]any{
		"name":         "V",
		"piece length": 1024,
		"meta version": 2,
		"pieces":       v1Pieces,
		"file tree": map[string]any{
			"a.bin": map[string]any{"": map[string]any{"length": 100}},  // piece 0
			"b.bin": map[string]any{"": map[string]any{"length": 2000}}, // pieces 1,2
			"c.bin": map[string]any{"": map[string]any{"length": 10}},   // piece 3
		},
	}
	r, err := NewTestResourceMap().parseTorrent(encodeTorrent(t, info))
	require.NoError(t, err)
	require.Len(t, r.Files, 3)
	pieces := splitPieces(v1Pieces)
	require.Equal(t, pieces[0:1], r.Files[0].Pieces)
	require.Equal(t, pieces[1:3], r.Files[1].Pieces, "b.bin starts at piece 1, not inside piece 0")
	require.Equal(t, pieces[3:4], r.Files[2].Pieces, "c.bin is piece 3; a running sum would put it in piece 2")
}
