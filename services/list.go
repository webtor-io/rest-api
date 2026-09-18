package services

import (
	"crypto/sha1"
	"fmt"
	"mime"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type ListOutputType string

const (
	ListOutputTypeList ListOutputType = "list"
	ListOutputTypeTree ListOutputType = "tree"
)

type List struct{}

type ListSortType string

const (
	ListSortTypeNone ListSortType = ""
	ListSortTypeName ListSortType = "name"
	ListSortTypeSize ListSortType = "size"
)

type ListGetArgs struct {
	Limit  int
	Offset int
	Output ListOutputType
	Path   []string
	Sort   ListSortType
}

type ParamGetter interface {
	Param(s string) string
	Query(s string) string
	QueryArray(s string) []string
	GetHeader(s string) string
}

func NewListGetArgs() *ListGetArgs {
	return &ListGetArgs{
		Output: ListOutputTypeList,
		Path:   []string{},
		Sort:   ListSortTypeNone,
	}
}

func ListGetArgsFromParams(g ParamGetter) (*ListGetArgs, error) {
	res := &ListGetArgs{}
	switch g.Query("output") {
	case "tree":
		res.Output = ListOutputTypeTree
	case "list":
		res.Output = ListOutputTypeList
	case "":
		res.Output = ListOutputTypeList
	default:
		return nil, errors.Errorf("failed to parse output, should be tree or list")
	}
	if g.Query("limit") == "" {
		res.Limit = 1000
	} else {
		limit, err := strconv.Atoi(g.Query("limit"))
		if err != nil {
			return nil, errors.Errorf("failed to parse limit, should be integer")
		}
		if limit > 1000 {
			return nil, errors.Errorf("failed to parse limit, should be less than 1000")
		}
		if limit < 1 {
			return nil, errors.Errorf("failed to parse limit, should be more than 1")
		}
		res.Limit = limit
	}
	if g.Query("offset") == "" {
		res.Offset = 0
	} else {
		offset, err := strconv.Atoi(g.Query("offset"))
		if err != nil {
			return nil, errors.Errorf("failed to parse offset, should be integer")
		}
		if offset < 0 {
			return nil, errors.Errorf("failed to parse offset, should be positive")
		}
		res.Offset = offset
	}
	path := strings.TrimLeft(strings.TrimRight(g.Query("path"), "/"), "/")

	if path == "" {
		res.Path = []string{}
	} else {
		res.Path = strings.Split(path, "/")
	}
	switch g.Query("sort") {
	case "name":
		res.Sort = ListSortTypeName
	case "size":
		res.Sort = ListSortTypeSize
	case "":
		res.Sort = ListSortTypeNone
	default:
		return nil, errors.Errorf("failed to parse sort, should be name or size")
	}
	return res, nil
}

func NewList() *List {
	return &List{}
}

func pathBeginsWith(source []string, start []string) bool {
	if len(start) == 0 {
		return true
	}
	check := true
	for i, p := range source {
		if len(start) <= i {
			break
		}
		if start[i] != p {
			check = false
		}
	}
	return check
}

func (s *List) buildRootItem(path []string, size int64) ListItem {
	fps := "/" + strings.Join(path, "/")
	return ListItem{
		ID:      fmt.Sprintf("%x", sha1.Sum([]byte(fps))),
		PathStr: fps,
		Path:    path,
		Type:    ListTypeDirectory,
		Size:    size,
	}
}

// dirKey extends the joined relative directory key by one component.
func dirKey(key, component string) string {
	if key == "" {
		return component
	}
	return key + "/" + component
}

func (s *List) buildList(r *Resource, args *ListGetArgs) ListResponse {
	var items []ListItem
	var size int64
	count := 0

	// Directory sizes in one pass over the files: O(files × depth). The
	// previous buildDirSize rescanned r.Files for every directory it emitted,
	// O(dirs × files) — 16k dirs × 184k files on a 193 GiB game-source
	// torrent was 3×10^9 path comparisons and 84 s of CPU for a single
	// list?limit=1 (rest-api OOM, 2026-09-18). Keyed by the path relative to
	// args.Path, which is also what the old prefix check was meant to
	// compare against.
	dirSizes := map[string]int64{}
	for _, f := range r.Files {
		if !pathBeginsWith(f.Path, args.Path) {
			continue
		}
		key := ""
		for _, v := range f.Path[len(args.Path) : len(f.Path)-1] {
			key = dirKey(key, v)
			dirSizes[key] += f.Size
		}
	}

	// Without a sort the response order is the walk order, so once
	// offset+limit items are collected nothing past them can be returned —
	// the rest only needs counting. Keeps list?limit=1 from materialising a
	// 200k-element []ListItem (130 MiB retained per request) to return one.
	pageFull := func() bool {
		return args.Sort == ListSortTypeNone && args.Limit != 0 && len(items) >= args.Offset+args.Limit
	}

	// seen tracks already-emitted directory prefixes by their joined path,
	// O(1) per check; dirs are emitted in first-seen order.
	seen := map[string]struct{}{}
	for i, f := range r.Files {
		if !pathBeginsWith(f.Path, args.Path) {
			continue
		}
		if len(f.Path) > len(args.Path) {
			rel := f.Path[len(args.Path) : len(f.Path)-1]
			key := ""
			for j, v := range rel {
				key = dirKey(key, v)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				count++
				if pageFull() {
					continue
				}
				fp := make([]string, 0, len(args.Path)+j+1)
				fp = append(fp, args.Path...)
				fp = append(fp, rel[:j+1]...)
				fps := "/" + strings.Join(fp, "/")
				items = append(items, ListItem{
					ID:      fmt.Sprintf("%x", sha1.Sum([]byte(fps))),
					Name:    v,
					Size:    dirSizes[key],
					PathStr: fps,
					Path:    fp,
					Type:    ListTypeDirectory,
				})
			}
		}
		size += f.Size
		count++
		if pageFull() {
			continue
		}
		items = append(items, s.buildFile(f, i))
	}

	// Sort items with folders first, then by selected criteria
	s.sortItems(items, args.Sort)

	// Apply pagination after sorting
	if args.Offset > 0 || (args.Limit != 0 && args.Offset+args.Limit < len(items)) {
		var limitted []ListItem
		for n, i := range items {
			if n >= args.Offset+args.Limit {
				break
			}
			if n >= args.Offset {
				limitted = append(limitted, i)
			}
		}
		items = limitted
	}

	return ListResponse{
		ListItem: s.buildRootItem(args.Path, size),
		Items:    items,
		Count:    count,
	}
}

func (s *List) sortItems(items []ListItem, sortType ListSortType) {
	if sortType == ListSortTypeNone {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		// Folders always come first
		if items[i].Type == ListTypeDirectory && items[j].Type == ListTypeFile {
			return true
		}
		if items[i].Type == ListTypeFile && items[j].Type == ListTypeDirectory {
			return false
		}

		// Both are same type, apply sorting criteria
		switch sortType {
		case ListSortTypeSize:
			if items[i].Size != items[j].Size {
				return items[i].Size > items[j].Size
			}
			// If sizes are equal, fall back to alphabetical
			return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		case ListSortTypeName:
			fallthrough
		default:
			return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
		}
	})
}

func (s *List) buildFile(f *File, idx int) ListItem {
	fps := "/" + strings.Join(f.Path, "/")
	name := f.Path[len(f.Path)-1]
	ext := strings.ToLower(strings.TrimLeft(filepath.Ext(name), "."))
	i := ListItem{
		ID:      fmt.Sprintf("%x", sha1.Sum([]byte(fps))),
		Name:    name,
		Size:    f.Size,
		PathStr: fps,
		Path:    f.Path,
		Type:    ListTypeFile,
		Ext:     ext,
		Index:   idx,
	}
	mf := getMediaFormatByExt(ext)
	if mf != Unknown {
		i.MediaFormat = mf
		i.MimeType = mime.TypeByExtension("." + ext)
	}
	return i
}

func (s *List) buildTree(r *Resource, args *ListGetArgs) ListResponse {
	items := []ListItem{}
	var size int64
	var dir *ListItem
	for i, f := range r.Files {
		if !pathBeginsWith(f.Path, args.Path) {
			continue
		}
		size += f.Size
		if len(args.Path)+1 == len(f.Path) {
			if dir != nil {
				items = append(items, *dir)
				dir = nil
			}
			items = append(items, s.buildFile(f, i))
		} else {
			fps := "/" + strings.Join(f.Path[0:len(args.Path)+1], "/")
			if dir != nil && dir.PathStr != fps {
				items = append(items, *dir)
				dir = nil
			}
			if dir == nil {
				dir = &ListItem{
					ID:      fmt.Sprintf("%x", sha1.Sum([]byte(fps))),
					Name:    f.Path[len(args.Path)],
					PathStr: fps,
					Path:    f.Path[0 : len(args.Path)+1],
					Type:    ListTypeDirectory,
				}
			}
			dir.Size += f.Size
		}
	}
	if dir != nil {
		items = append(items, *dir)
		dir = nil
	}

	// Sort items with folders first, then by selected criteria
	s.sortItems(items, args.Sort)

	count := len(items)

	if args.Offset > 0 || (args.Limit != 0 && args.Offset+args.Limit < len(items)) {
		var limitted []ListItem
		for n, i := range items {
			if n >= args.Offset+args.Limit {
				break
			}
			if n >= args.Offset {
				limitted = append(limitted, i)
			}
		}
		items = limitted
	}

	return ListResponse{
		ListItem: s.buildRootItem(args.Path, size),
		Items:    items,
		Count:    count,
	}
}

func (s *List) Get(r *Resource, args *ListGetArgs) (ListResponse, error) {
	if args.Output == ListOutputTypeList {
		return s.buildList(r, args), nil
	}
	return s.buildTree(r, args), nil
}
