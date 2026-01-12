package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestList_sortItems_FoldersFirst(t *testing.T) {
	l := NewList()

	items := []ListItem{
		{Name: "file1.txt", Type: ListTypeFile, Size: 100},
		{Name: "aFolder", Type: ListTypeDirectory, Size: 500},
		{Name: "file2.txt", Type: ListTypeFile, Size: 200},
		{Name: "bFolder", Type: ListTypeDirectory, Size: 300},
	}

	l.sortItems(items, ListSortTypeName)

	// Verify folders come first
	assert.Equal(t, ListTypeDirectory, items[0].Type)
	assert.Equal(t, ListTypeDirectory, items[1].Type)
	assert.Equal(t, ListTypeFile, items[2].Type)
	assert.Equal(t, ListTypeFile, items[3].Type)

	// Verify alphabetical order within each type
	assert.Equal(t, "aFolder", items[0].Name)
	assert.Equal(t, "bFolder", items[1].Name)
	assert.Equal(t, "file1.txt", items[2].Name)
	assert.Equal(t, "file2.txt", items[3].Name)
}

func TestList_sortItems_AlphabeticalCaseInsensitive(t *testing.T) {
	l := NewList()

	items := []ListItem{
		{Name: "Zebra.txt", Type: ListTypeFile, Size: 100},
		{Name: "apple.txt", Type: ListTypeFile, Size: 200},
		{Name: "Banana.txt", Type: ListTypeFile, Size: 150},
	}

	l.sortItems(items, ListSortTypeName)

	assert.Equal(t, "apple.txt", items[0].Name)
	assert.Equal(t, "Banana.txt", items[1].Name)
	assert.Equal(t, "Zebra.txt", items[2].Name)
}

func TestList_sortItems_BySize(t *testing.T) {
	l := NewList()

	items := []ListItem{
		{Name: "small.txt", Type: ListTypeFile, Size: 100},
		{Name: "large.txt", Type: ListTypeFile, Size: 500},
		{Name: "medium.txt", Type: ListTypeFile, Size: 300},
		{Name: "folder", Type: ListTypeDirectory, Size: 1000},
	}

	l.sortItems(items, ListSortTypeSize)

	// Folder should still come first
	assert.Equal(t, ListTypeDirectory, items[0].Type)
	assert.Equal(t, "folder", items[0].Name)

	// Files sorted by size (largest first)
	assert.Equal(t, "large.txt", items[1].Name)
	assert.Equal(t, int64(500), items[1].Size)
	assert.Equal(t, "medium.txt", items[2].Name)
	assert.Equal(t, int64(300), items[2].Size)
	assert.Equal(t, "small.txt", items[3].Name)
	assert.Equal(t, int64(100), items[3].Size)
}

func TestList_sortItems_SizeTieBreaker(t *testing.T) {
	l := NewList()

	items := []ListItem{
		{Name: "zebra.txt", Type: ListTypeFile, Size: 100},
		{Name: "apple.txt", Type: ListTypeFile, Size: 100},
	}

	l.sortItems(items, ListSortTypeSize)

	// When sizes are equal, should fall back to alphabetical
	assert.Equal(t, "apple.txt", items[0].Name)
	assert.Equal(t, "zebra.txt", items[1].Name)
}
