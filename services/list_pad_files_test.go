package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// BEP 47 padding files are left out of every listing shape — list, tree,
// FindByID — and out of directory and root sizes, while the files after
// them keep their torrent file index (Index is what /export/<idx> and the
// Stremio Library's persisted file_idx address).
func padResource() *Resource {
	return &Resource{
		Files: []*File{
			{Path: []string{"T", "S01E01.mkv"}, Size: 100},
			{Path: []string{"T", ".pad", "1234"}, Size: 1234, Pad: true},
			{Path: []string{"T", "S01E02.mkv"}, Size: 200},
			{Path: []string{"T", ".pad", "99"}, Size: 99, Pad: true},
			{Path: []string{"T", "Subs", "S01E02.srt"}, Size: 7},
		},
	}
}

func TestList_buildList_HidesPadFilesKeepsIndex(t *testing.T) {
	l := NewList()
	resp := l.buildList(padResource(), &ListGetArgs{Path: []string{"T"}, Sort: ListSortTypeNone})

	var names []string
	index := map[string]int{}
	for _, it := range resp.Items {
		names = append(names, it.Name)
		if it.Type == ListTypeFile {
			index[it.Name] = it.Index
		}
	}
	// The list shape is flat and recursive: every file under the path plus
	// each directory once. Nothing from .pad in either role.
	assert.ElementsMatch(t, []string{"S01E01.mkv", "S01E02.mkv", "Subs", "S01E02.srt"}, names, "no .pad directory, no pad files")
	assert.Equal(t, 4, resp.Count)
	assert.Equal(t, 0, index["S01E01.mkv"])
	assert.Equal(t, 2, index["S01E02.mkv"], "index counts the skipped pad file")
	assert.Equal(t, int64(307), resp.ListItem.Size, "root size excludes padding")
}

func TestList_buildTree_HidesPadFiles(t *testing.T) {
	l := NewList()
	resp := l.buildTree(padResource(), &ListGetArgs{Path: []string{"T"}, Sort: ListSortTypeNone})
	var names []string
	for _, it := range resp.Items {
		names = append(names, it.Name)
		if it.Type == ListTypeFile {
			assert.NotEqual(t, ".pad", it.Path[1])
		}
	}
	assert.ElementsMatch(t, []string{"S01E01.mkv", "S01E02.mkv", "Subs"}, names)
	assert.Equal(t, int64(307), resp.ListItem.Size)
}

func TestList_FindByID_SkipsPadFiles(t *testing.T) {
	l := NewList()
	r := padResource()
	root, ok := l.FindByID(r, l.buildRootItem([]string{}, 0).ID)
	assert.True(t, ok)
	assert.Equal(t, int64(307), root.Size)

	_, ok = l.FindByID(r, l.buildFile(r.Files[1], 1).ID)
	assert.False(t, ok, "a pad file is not addressable")

	it, ok := l.FindByID(r, l.buildFile(r.Files[2], 2).ID)
	assert.True(t, ok)
	assert.Equal(t, 2, it.Index)
}

func TestIsPadFile(t *testing.T) {
	assert.True(t, isPadFile("p", []string{"whatever"}), "attr p")
	assert.True(t, isPadFile("", []string{".pad", "1048576"}), "libtorrent name")
	assert.True(t, isPadFile("", []string{"_____padding_file_0_if you see this file, please update to BitComet 0.85 or above____"}), "BitComet name")
	assert.False(t, isPadFile("", []string{"pad", "1048576"}))
	assert.False(t, isPadFile("", []string{"S01", ".pad"}), "only a leading .pad directory")
	assert.False(t, isPadFile("x", nil))
}
