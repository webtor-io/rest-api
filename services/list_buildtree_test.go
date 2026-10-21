package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A directory whose files are not adjacent in the torrent is one item with
// their summed size. A pack ordered by file size interleaves its seasons;
// buildTree used to merge only adjacent runs and listed one "S01" per file
// (support c5bd5264, 2026-10-21).
func TestList_buildTree_NonAdjacentDirFiles(t *testing.T) {
	l := NewList()
	r := &Resource{
		Files: []*File{
			{Path: []string{"Pack", "S01", "e1.mp4"}, Size: 40},
			{Path: []string{"Pack", "S02", "e1.mp4"}, Size: 30},
			{Path: []string{"Pack", "notes.txt"}, Size: 1},
			{Path: []string{"Pack", "S01", "e2.mp4"}, Size: 20},
			{Path: []string{"Pack", "S02", "e2.mp4"}, Size: 10},
		},
	}
	resp := l.buildTree(r, &ListGetArgs{Path: []string{"Pack"}, Sort: ListSortTypeNone})

	var got []string
	sizes := map[string]int64{}
	for _, it := range resp.Items {
		got = append(got, it.PathStr)
		sizes[it.PathStr] = it.Size
	}
	assert.Equal(t, []string{"/Pack/S01", "/Pack/S02", "/Pack/notes.txt"}, got, "each directory once, first-seen order")
	assert.Equal(t, int64(60), sizes["/Pack/S01"])
	assert.Equal(t, int64(40), sizes["/Pack/S02"])
	assert.Equal(t, 3, resp.Count)
	assert.Equal(t, int64(101), resp.ListItem.Size)
}
