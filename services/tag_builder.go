package services

import (
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

type BaseTagBulder struct {
	ub *URLBuilder
	r  *Resource
	i  *ListItem
	g  ParamGetter
}

type VideoTagBuider struct {
	l *List
	BaseTagBulder
}

type AudioTagBuider struct {
	BaseTagBulder
}

type ImageTagBuider struct {
	BaseTagBulder
}

func (s *TagBuilder) Build(r *Resource, i *ListItem, g ParamGetter) (*ExportTag, error) {
	btb := BaseTagBulder{
		ub: s.ub,
		r:  r,
		i:  i,
		g:  g,
	}
	switch i.MediaFormat {
	case Video:
		vtb := VideoTagBuider{
			l:             s.l,
			BaseTagBulder: btb,
		}
		return vtb.Build()
	case Audio:
		atb := AudioTagBuider{
			BaseTagBulder: btb,
		}
		return atb.Build()
	case Image:
		itb := ImageTagBuider{
			BaseTagBulder: btb,
		}
		return itb.Build()
	}
	return nil, nil
}

func (s *BaseTagBulder) BuildURL(i *ListItem) (*MyURL, error) {
	return s.ub.Build(s.r, i, s.g, ExportTypeStream)
}

func (s *BaseTagBulder) BuildSource(u *MyURL) *ExportSource {
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
func (s *BaseTagBulder) BuildAVTag(n ExportTagName) (*ExportTag, error) {
	url, err := s.BuildURL(s.i)
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

func (s *VideoTagBuider) BuildTrack(i *ListItem) (*ExportTrack, error) {
	u, err := s.BuildURL(i)
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

func (s *VideoTagBuider) BuildAttachedResources(et *ExportTag) (*ExportTag, error) {
	r, err := s.l.Get(s.r, &ListGetArgs{})
	if err != nil {
		return nil, err
	}
	tt := []ExportTrack{}
	for _, v := range r.Items {
		if v.SameDirectory(s.i) && v.MediaFormat == Subtitle && strings.HasPrefix(v.Name, strings.TrimSuffix(s.i.Name, "."+s.i.Ext)) {
			t, err := s.BuildTrack(&v)
			if err != nil {
				return nil, err
			}
			if t != nil {
				tt = append(tt, *t)
			}
		}
		if v.SameDirectory(s.i) && v.MediaFormat == Image && et.Poster == "" {
			u, err := s.BuildURL(&v)
			if err != nil {
				return nil, err
			}
			et.Poster = u.String()
		}
	}
	et.Tracks = tt
	return et, nil
}

func (s *VideoTagBuider) Build() (*ExportTag, error) {
	et, err := s.BuildAVTag(ExportTagNameVideo)
	if err != nil {
		return nil, err
	}
	return s.BuildAttachedResources(et)
}

func (s *AudioTagBuider) Build() (*ExportTag, error) {
	return s.BuildAVTag(ExportTagNameAudio)
}

func (s *ImageTagBuider) Build() (et *ExportTag, err error) {
	src, err := s.BuildURL(s.i)
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
