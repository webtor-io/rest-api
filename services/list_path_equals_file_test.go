package services

import "testing"

// Listing with path equal to a file's own path (single-file torrents opened
// directly, file pages) must return that file, as it did before 45c808f.
func TestList_buildList_PathEqualsFile(t *testing.T) {
	l := NewList()
	r := &Resource{Files: []*File{{Path: []string{"Movie.mkv"}, Size: 7}, {Path: []string{"dir", "x.srt"}, Size: 1}}}
	for _, out := range []ListOutputType{ListOutputTypeList, ListOutputTypeTree} {
		resp, err := l.Get(r, &ListGetArgs{Path: []string{"Movie.mkv"}, Output: out, Sort: ListSortTypeNone, Limit: 10})
		if err != nil {
			t.Fatalf("output=%v: %v", out, err)
		}
		if len(resp.Items) != 1 || resp.Items[0].Name != "Movie.mkv" || resp.ListItem.Size != 7 {
			t.Errorf("output=%v: items=%+v size=%d", out, resp.Items, resp.ListItem.Size)
		}
	}
}
