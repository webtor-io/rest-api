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
			{Path: []string{"zzz.txt"}, Size: 1},      // natural idx 0
			{Path: []string{"dir", "a.mkv"}, Size: 2}, // natural idx 1
			{Path: []string{"dir", "b.mkv"}, Size: 3}, // natural idx 2
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

// Directory sizes under a non-root path. The old buildDirSize compared a
// prefix relative to args.Path against absolute file paths, so every nested
// directory of a sub-path listing came back with size 0 (confirmed on a real
// 184k-file manifest, 2026-09-18). Sizes must be the sum of the files below
// the directory, exactly as for path=/.
func TestList_buildList_NestedPathDirSize(t *testing.T) {
	l := NewList()
	r := &Resource{
		Files: []*File{
			{Path: []string{"a", "b", "f1"}, Size: 10},
			{Path: []string{"a", "b", "c", "f2"}, Size: 20},
			{Path: []string{"a", "d", "f3"}, Size: 30},
			{Path: []string{"x", "f4"}, Size: 1000}, // outside args.Path
		},
	}
	resp := l.buildList(r, &ListGetArgs{Path: []string{"a"}, Sort: ListSortTypeNone})

	sizes := map[string]int64{}
	for _, it := range resp.Items {
		if it.Type == ListTypeDirectory {
			sizes[it.PathStr] = it.Size
		}
	}
	assert.Equal(t, int64(30), sizes["/a/b"])
	assert.Equal(t, int64(20), sizes["/a/b/c"])
	assert.Equal(t, int64(30), sizes["/a/d"])
	assert.Len(t, sizes, 3)
	assert.Equal(t, int64(60), resp.ListItem.Size, "root size excludes files outside the path")
	assert.Equal(t, 6, resp.Count, "3 dirs + 3 files")
}

// Without a sort, a page must equal the same slice of the full listing and
// Count must still be the full item count — the early stop in buildList
// (which is what keeps list?limit=1 from materialising 200k items) must not
// change what a client sees.
func TestList_buildList_PaginationWithoutSortMatchesFullList(t *testing.T) {
	l := NewList()
	r := &Resource{
		Files: []*File{
			{Path: []string{"a", "b", "f1"}, Size: 10},
			{Path: []string{"a", "b", "f2"}, Size: 20},
			{Path: []string{"a", "c", "f3"}, Size: 30},
			{Path: []string{"root.txt"}, Size: 5},
			{Path: []string{"z", "f5"}, Size: 7},
		},
	}
	full := l.buildList(r, &ListGetArgs{Path: []string{}, Sort: ListSortTypeNone})
	assert.Len(t, full.Items, full.Count)

	for _, tc := range []struct{ offset, limit int }{{0, 1}, {1, 2}, {3, 10}, {0, 0}} {
		page := l.buildList(r, &ListGetArgs{Path: []string{}, Sort: ListSortTypeNone, Offset: tc.offset, Limit: tc.limit})
		end := len(full.Items)
		if tc.limit != 0 && tc.offset+tc.limit < end {
			end = tc.offset + tc.limit
		}
		assert.Equal(t, full.Items[tc.offset:end], page.Items, "offset=%d limit=%d", tc.offset, tc.limit)
		assert.Equal(t, full.Count, page.Count, "offset=%d limit=%d", tc.offset, tc.limit)
		assert.Equal(t, full.ListItem.Size, page.ListItem.Size, "offset=%d limit=%d", tc.offset, tc.limit)
	}
}
