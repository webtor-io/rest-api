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
