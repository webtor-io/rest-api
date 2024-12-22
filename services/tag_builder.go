package services

import (
	"context"
	"mime"
	"path/filepath"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

type TagBuilder struct {
	ub *URLBuilder
	l  *List
}

func NewTagBuilder(ub *URLBuilder, l *List) *TagBuilder {
	return &TagBuilder{
		ub: ub,
		l:  l,
	}
}

type BaseTagBuilder struct {
	ub *URLBuilder
	r  *Resource
	i  *ListItem
	g  ParamGetter
}

type VideoTagBuider struct {
	l *List
	BaseTagBuilder
}

type AudioTagBuider struct {
	BaseTagBuilder
}

type ImageTagBuider struct {
	BaseTagBuilder
}

func (s *TagBuilder) Build(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportTag, error) {
	btb := BaseTagBuilder{
		ub: s.ub,
		r:  r,
		i:  i,
		g:  g,
	}
	switch i.MediaFormat {
	case Video:
		vtb := VideoTagBuider{
			l:              s.l,
			BaseTagBuilder: btb,
		}
		return vtb.Build(ctx)
	case Audio:
		atb := AudioTagBuider{
			BaseTagBuilder: btb,
		}
		return atb.Build(ctx)
	case Image:
		itb := ImageTagBuider{
			BaseTagBuilder: btb,
		}
		return itb.Build(ctx)
	}
	return nil, nil
}

func (s *BaseTagBuilder) BuildURL(ctx context.Context, i *ListItem) (*MyURL, error) {
	return s.ub.Build(ctx, s.r, i, s.g, ExportTypeStream)
}

func (s *BaseTagBuilder) BuildSource(u *MyURL) *ExportSource {
	ext := filepath.Ext(u.Path)
	t := ""
	if strings.HasSuffix(u.Path, "index.m3u8") {
		t = "application/vnd.apple.mpegurl"
	} else {
		t = mime.TypeByExtension(ext)
	}
	return &ExportSource{
		Src:  u.String(),
		Type: t,
	}
}
func (s *BaseTagBuilder) BuildAVTag(ctx context.Context, n ExportTagName) (*ExportTag, error) {
	url, err := s.BuildURL(ctx, s.i)
	if err != nil {
		return nil, err
	}
	src := s.BuildSource(url)
	preload := ExportPreloadTypeNone
	if url.cached {
		preload = ExportPreloadTypeAuto
	}
	return &ExportTag{
		Name:    n,
		Preload: preload,
		Sources: []ExportSource{
			*src,
		},
	}, nil
}

func (s *ListItem) SameDirectory(li *ListItem) bool {
	for i := 0; i < len(s.Path)-1; i++ {
		for j := 0; j < len(li.Path)-1; j++ {
			if i != j {
				continue
			}
			if s.Path[i] != li.Path[j] {
				return false
			}
		}
	}
	return true
}

func (s *VideoTagBuider) BuildTrack(ctx context.Context, i *ListItem) (*ExportTrack, error) {
	u, err := s.BuildURL(ctx, i)
	if err != nil {
		return nil, err
	}
	lc := filepath.Ext(strings.TrimSuffix(i.Name, filepath.Ext(i.Name)))
	label := ""
	srclang := ""
	if lc != "" {
		lc = strings.TrimLeft(lc, ".")
		t, err := language.Parse(lc)
		if err == nil {
			srclang = t.String()
			label = display.English.Tags().Name(t)
		}
	}
	return &ExportTrack{
		Kind:    ExportKindTypeSubtitles,
		Src:     u.String(),
		Label:   label,
		SrcLang: srclang,
	}, nil
}

func (s *VideoTagBuider) BuildAttachedResources(ctx context.Context, et *ExportTag) (*ExportTag, error) {
	r, err := s.l.Get(s.r, &ListGetArgs{
		Path: s.i.Path[0 : len(s.i.Path)-1],
	})
	if err != nil {
		return nil, err
	}
	tt := []ExportTrack{}
	for _, v := range r.Items {
		if v.SameDirectory(s.i) && v.MediaFormat == Subtitle && strings.HasPrefix(v.Name, strings.TrimSuffix(s.i.Name, "."+s.i.Ext)) {
			t, err := s.BuildTrack(ctx, &v)
			if err != nil {
				return nil, err
			}
			if t != nil {
				tt = append(tt, *t)
			}
		}
		if v.SameDirectory(s.i) && v.MediaFormat == Image && et.Poster == "" {
			u, err := s.BuildURL(ctx, &v)
			if err != nil {
				return nil, err
			}
			et.Poster = u.String()
		}
	}
	et.Tracks = tt
	return et, nil
}

func (s *VideoTagBuider) Build(ctx context.Context) (*ExportTag, error) {
	et, err := s.BuildAVTag(ctx, ExportTagNameVideo)
	if err != nil {
		return nil, err
	}
	return s.BuildAttachedResources(ctx, et)
}

func (s *AudioTagBuider) Build(ctx context.Context) (*ExportTag, error) {
	return s.BuildAVTag(ctx, ExportTagNameAudio)
}

func (s *ImageTagBuider) Build(ctx context.Context) (et *ExportTag, err error) {
	src, err := s.BuildURL(ctx, s.i)
	if err != nil {
		return nil, err
	}
	et = &ExportTag{
		Alt:  s.i.Name,
		Src:  src.String(),
		Name: ExportTagNameImage,
	}
	return
}
