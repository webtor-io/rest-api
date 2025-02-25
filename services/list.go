package services

import (
	"crypto/sha1"
	"fmt"
	"mime"
	"path/filepath"
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

type ListGetArgs struct {
	Limit  int
	Offset int
	Output ListOutputType
	Path   []string
}

type ParamGetter interface {
	Param(s string) string
	Query(s string) string
	GetHeader(s string) string
}

func NewListGetArgs() *ListGetArgs {
	return &ListGetArgs{
		Output: ListOutputTypeList,
		Path:   []string{},
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

// https://stackoverflow.com/a/15312097
func testEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *List) buildDirSize(r *Resource, prefix []string) int64 {
	var size int64
	for _, f := range r.Files {
		if !pathBeginsWith(f.Path, prefix) {
			if size > 0 {
				break
			}
			continue
		}
		size += f.Size
	}
	return size
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

func (s *List) buildList(r *Resource, args *ListGetArgs) ListResponse {
	var items []ListItem
	var count int
	var size int64
	var dirs [][]string
	for _, f := range r.Files {
		if !pathBeginsWith(f.Path, args.Path) {
			continue
		}
		if len(f.Path) > len(args.Path) {
			var p []string
			for _, v := range f.Path[len(args.Path) : len(f.Path)-1] {
				p = append(p, v)
				found := false
				for _, d := range dirs {
					if testEq(p, d) {
						found = true
						break
					}
				}
				if !found {
					dirs = append(dirs, p)
					count++
					if count-1 < args.Offset || (args.Limit != 0 && count > args.Offset+args.Limit) {
						continue
					}
					var fp []string
					fp = append(fp, args.Path...)
					fp = append(fp, p...)
					fps := "/" + strings.Join(fp, "/")
					items = append(items, ListItem{
						ID:      fmt.Sprintf("%x", sha1.Sum([]byte(fps))),
						Name:    p[len(p)-1],
						Size:    s.buildDirSize(r, p),
						PathStr: fps,
						Path:    fp,
						Type:    ListTypeDirectory,
					})
				}
			}
		}
		count++
		size += f.Size
		if count-1 < args.Offset || (args.Limit != 0 && count > args.Offset+args.Limit) {
			continue
		}
		items = append(items, s.buildFile(f))
	}

	return ListResponse{
		ListItem: s.buildRootItem(args.Path, size),
		Items:    items,
		Count:    count,
	}
}

func (s *List) buildFile(f *File) ListItem {
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
	for _, f := range r.Files {
		if !pathBeginsWith(f.Path, args.Path) {
			continue
		}
		size += f.Size
		if len(args.Path)+1 == len(f.Path) {
			if dir != nil {
				items = append(items, *dir)
				dir = nil
			}
			items = append(items, s.buildFile(f))
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
