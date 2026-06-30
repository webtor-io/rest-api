package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Verifies the map-based directory dedup in buildList emits each directory
// prefix exactly once, in first-seen order, with all files present.
func TestList_buildList_DirDedup(t *testing.T) {
	l := NewList()
	r := &Resource{
		Files: []*File{
			{Path: []string{"a", "b", "file1.txt"}, Size: 10},
			{Path: []string{"a", "b", "file2.txt"}, Size: 20}, // a, a/b already seen
			{Path: []string{"a", "c", "file3.txt"}, Size: 30}, // a seen, a/c new
			{Path: []string{"root.txt"}, Size: 5},
		},
	}
	args := &ListGetArgs{Path: []string{}, Sort: ListSortTypeNone}
	resp := l.buildList(r, args)

	dirCount := map[string]int{}
	fileNames := map[string]bool{}
	for _, it := range resp.Items {
		if it.Type == ListTypeDirectory {
			dirCount[it.PathStr]++
		} else {
			fileNames[it.Name] = true
		}
	}

	// each directory prefix exactly once (no quadratic-dedup regressions)
	assert.Equal(t, 1, dirCount["/a"], "/a must appear once")
	assert.Equal(t, 1, dirCount["/a/b"], "/a/b must appear once")
	assert.Equal(t, 1, dirCount["/a/c"], "/a/c must appear once")
	assert.Len(t, dirCount, 3, "exactly 3 distinct dirs")

	// all files present
	for _, n := range []string{"file1.txt", "file2.txt", "file3.txt", "root.txt"} {
		assert.True(t, fileNames[n], "file %s present", n)
	}

	// total size summed over all files
	assert.Equal(t, int64(65), resp.ListItem.Size)
}

// Verifies ListItem.Index carries each file's position in the torrent's
// natural file order (r.Files), independent of the folders-first/name sort
// applied to the response. A client (web-ui Stremio Library) can then store
// Index and pass it straight to /export/<idx> without re-deriving it from
// the sorted, paginated list order.
func TestList_buildList_FileIndex(t *testing.T) {
	l := NewList()
	// Natural order is deliberately NOT the sorted order: a root-level file
	// comes first (index 0), then files nested under a directory. After the
	// folders-first sort the directory bubbles above "zzz.txt", so list
	// position != natural index — only Index stays anchored to r.Files.
	r := &Resource{
		Files: []*File{
			{Path: []string{"zzz.txt"}, Size: 1},        // natural idx 0
			{Path: []string{"dir", "a.mkv"}, Size: 2},   // natural idx 1
			{Path: []string{"dir", "b.mkv"}, Size: 3},   // natural idx 2
		},
	}
	args := &ListGetArgs{Path: []string{}, Sort: ListSortTypeName}
	resp := l.buildList(r, args)

	wantIdx := map[string]int{"zzz.txt": 0, "a.mkv": 1, "b.mkv": 2}
	got := map[string]int{}
	for _, it := range resp.Items {
		if it.Type != ListTypeFile {
			continue
		}
		got[it.Name] = it.Index
	}
	for name, idx := range wantIdx {
		assert.Equal(t, idx, got[name], "file %s must carry natural index %d", name, idx)
	}

	// Sanity: the sort really did reorder, so the test is meaningful — the
	// directory's files precede the root file despite the root file being
	// natural index 0.
	var order []string
	for _, it := range resp.Items {
		if it.Type == ListTypeFile {
			order = append(order, it.Name)
		}
	}
	assert.Equal(t, []string{"a.mkv", "b.mkv", "zzz.txt"}, order, "folders-first sort should reorder files")
}
