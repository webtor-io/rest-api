package services

import (
	"crypto/sha1"
	"fmt"
	"net/url"
	"strings"

	"github.com/urfave/cli"
)

type MyURL struct {
	url.URL
	cached       bool
	transcode    bool
	multibitrate bool
}

func (s *MyURL) BuildExportMeta() *ExportMeta {
	return &ExportMeta{
		Cache:        s.cached,
		Transcode:    s.transcode,
		Multibitrate: s.multibitrate,
	}
}

type URLBuilder struct {
	cpm    *CompletedPiecesMap
	tdm    *TranscodeDoneMap
	sd     *Subdomains
	domain string
	ssl    bool
}

func NewURLBuilder(c *cli.Context, cpm *CompletedPiecesMap, tdm *TranscodeDoneMap, sd *Subdomains) *URLBuilder {
	return &URLBuilder{
		cpm:    cpm,
		tdm:    tdm,
		sd:     sd,
		domain: c.String(exportDomainFlag),
		ssl:    c.BoolT(exportSSLFlag),
	}
}

func (s *URLBuilder) Build(r *Resource, i *ListItem, g ParamGetter, et ExportType) (*MyURL, error) {
	bubc := BaseURLBuilder{
		cpm:    s.cpm,
		sd:     s.sd,
		r:      r,
		i:      i,
		g:      g,
		domain: s.domain,
		ssl:    s.ssl,
	}
	switch et {
	case ExportTypeDownload:
		dub := &DownloadURLBuilder{
			BaseURLBuilder: bubc,
		}
		return dub.Build()
	case ExportTypeStream:
		sub := &StreamURLBuilder{
			tdm:            s.tdm,
			BaseURLBuilder: bubc,
		}
		return sub.Build()
	case ExportTypeTorrentStat:
		sub := &TorrentStatURLBuilder{
			BaseURLBuilder: bubc,
		}
		return sub.Build()
	case ExportTypeSubtitles:
		sub := &SubtitlesURLBuilder{
			BaseURLBuilder: bubc,
		}
		return sub.Build()
	case ExportTypeMediaProbe:
		sub := &MediaProbeURLBuilder{
			StreamURLBuilder: StreamURLBuilder{
				tdm:            s.tdm,
				BaseURLBuilder: bubc,
			},
		}
		return sub.Build()
	}
	return nil, nil
}

type BaseURLBuilder struct {
	cpm    *CompletedPiecesMap
	sd     *Subdomains
	r      *Resource
	i      *ListItem
	g      ParamGetter
	domain string
	ssl    bool
}

type DownloadURLBuilder struct {
	BaseURLBuilder
}

type StreamURLBuilder struct {
	tdm *TranscodeDoneMap
	BaseURLBuilder
}

type TorrentStatURLBuilder struct {
	BaseURLBuilder
}

type SubtitlesURLBuilder struct {
	BaseURLBuilder
}

type MediaProbeURLBuilder struct {
	StreamURLBuilder
}

type ServiceType string

const (
	ServiceTypeArchive                    ServiceType = "arch"
	ServiceTypeDownloadProgress           ServiceType = "dp"
	ServiceTypeTorrentCache               ServiceType = "tc"
	ServiceTypeMultibitrateTranscodeCache ServiceType = "mtrc"
	ServiceTypeTranscodeCache             ServiceType = "trc"
	ServiceTypeTranscode                  ServiceType = "hls"
	ServiceTypeVOD                        ServiceType = "vod"
	ServiceTypeSRT2VTT                    ServiceType = "vtt"
	ServiceTypeVideoInfo                  ServiceType = "vi"
)

const ServiceSeparator = "~"

func (s *BaseURLBuilder) BuildBaseURL(i *MyURL) (u *MyURL, err error) {
	u = i
	u.Path = "/" + s.r.ID + "/" + strings.TrimRight(s.r.Name+s.i.PathStr, "/")
	pieces, err := s.cpm.Get(s.r.ID)
	if err != nil {
		return nil, err
	}

	cached := false
	if len(pieces) > 0 {
		for _, f := range s.r.Files {
			if pathBeginsWith(f.Path, s.i.Path) && pieces.HasAny(f.Pieces) {
				cached = true
				break
			}
		}
	}
	if cached {
		u.Path += ServiceSeparator + string(ServiceTypeTorrentCache) + "/" + s.GetLastName()
		u.cached = true
	}
	q := u.Query()
	if s.g.Query("token") != "" {
		q.Add("token", s.g.Query("token"))
	} else if s.g.GetHeader("X-Token") != "" {
		q.Add("token", s.g.GetHeader("X-Token"))
	}
	if s.g.Query("user-id") != "" {
		q.Add("user-id", s.g.Query("user-id"))
	} else if s.g.GetHeader("X-User-Id") != "" {
		q.Add("user-id", s.g.GetHeader("X-User-Id"))
	}
	if s.g.Query("request-id") != "" {
		q.Add("request-id", s.g.Query("request-id"))
	} else if s.g.GetHeader("X-Request-Id") != "" {
		q.Add("request-id", s.g.GetHeader("X-Request-Id"))
	}
	if s.g.Query("api-key") != "" {
		q.Add("api-key", s.g.Query("api-key"))
	} else if s.g.GetHeader("X-Api-Key") != "" {
		q.Add("api-key", s.g.GetHeader("X-Api-Key"))
	}
	u.RawQuery = q.Encode()
	return
}

func (s *BaseURLBuilder) BuildScheme(i *MyURL) (u *MyURL) {
	u = i
	if u.Host == "" {
		return
	}
	if s.ssl {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	return
}

func (s *BaseURLBuilder) BuildDomain(i *MyURL) (u *MyURL, err error) {
	u = i
	if s.domain == "" {
		return u, nil
	}
	domain := s.domain
	pool := "seeder"
	if u.cached {
		pool = "cache"
	}
	subs, err := s.sd.Get(s.r.ID, pool)
	if err != nil {
		return nil, err
	}
	if len(subs) > 0 {
		domain = subs[0] + "." + domain
	}
	u.Host = domain
	return
}

func (s *BaseURLBuilder) GetLastName() string {
	p := []string{s.r.Name}
	p = append(p, s.i.Path...)
	return p[len(p)-1]
}

func (s *DownloadURLBuilder) BuildDownloadURL(i *MyURL) (u *MyURL, err error) {
	u = i
	l := s.GetLastName()
	if s.i.Type == ListTypeDirectory {
		l += ".zip"
		u.Path += ServiceSeparator + string(ServiceTypeArchive) + "/" + l
	}
	if s.g.Query("download-progress") == "true" {
		u.Path += ServiceSeparator + string(ServiceTypeDownloadProgress) + "/" + l
	}
	q := u.Query()
	q.Add("download", "true")
	u.RawQuery = q.Encode()
	return
}

func (s *DownloadURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildDownloadURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	s.BuildScheme(u)
	return
}

func (s *StreamURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildStreamURL(u, "/index.m3u8")
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	s.BuildScheme(u)
	return
}

func (s *StreamURLBuilder) BuildTranscodeCacheURL(i *MyURL, multi bool, suffix string) (u *MyURL, ok bool, err error) {
	u = i
	prefix := "transcoder"
	svc := ServiceTypeTranscodeCache
	u.multibitrate = false
	if multi {
		prefix = "mb-transcoder"
		u.multibitrate = true
		svc = ServiceTypeMultibitrateTranscodeCache
	}
	c, err := s.tdm.Get(prefix, s.r.ID, "/"+s.r.Name+s.i.PathStr)
	if err != nil {
		return nil, false, err
	}
	if c {
		u.cached = true
		u.transcode = true
		u.Path += ServiceSeparator + string(svc) + suffix
		return u, true, nil
	}
	return u, false, nil
}

func (s *StreamURLBuilder) BuildTranscodeURL(i *MyURL, suffix string) (u *MyURL) {
	u = i
	u.Path += ServiceSeparator + string(ServiceTypeTranscode) + suffix
	u.transcode = true
	return u
}

func (s *StreamURLBuilder) BuildVODURL(i *MyURL, suffix string) (u *MyURL) {
	u = i
	u.Path += ServiceSeparator + string(ServiceTypeVOD) + "/hls/" + fmt.Sprintf("%x", sha1.Sum([]byte(s.r.ID+s.i.ID))) + suffix
	return u
}

func (s *StreamURLBuilder) BuildVideoStreamURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	u, ok, err := s.BuildTranscodeCacheURL(u, true, suffix)
	if err != nil || ok {
		return
	}
	u, ok, err = s.BuildTranscodeCacheURL(u, false, suffix)
	if err != nil || ok {
		return
	}
	if shouldTranscode(s.i.Ext) {
		u = s.BuildTranscodeURL(i, suffix)
		return
	} else {
		u = s.BuildVODURL(i, suffix)
	}
	return
}

func (s *StreamURLBuilder) BuildAudioStreamURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	u, ok, err := s.BuildTranscodeCacheURL(u, false, suffix)
	if err != nil || ok {
		return
	}
	if shouldTranscode(s.i.Ext) {
		u = s.BuildTranscodeURL(i, suffix)
		return
	}
	return
}

func (s *StreamURLBuilder) BuildSRT2VTTURL(i *MyURL) (u *MyURL) {
	u = i
	l := strings.TrimSuffix(s.GetLastName(), "srt") + "vtt"
	u.Path += ServiceSeparator + string(ServiceTypeSRT2VTT) + "/" + l
	return u
}

func (s *StreamURLBuilder) BuildSubtitleStreamURL(i *MyURL) (u *MyURL, err error) {
	if s.i.Ext == "srt" {
		u = s.BuildSRT2VTTURL(i)
	}
	return
}

func (s *StreamURLBuilder) BuildStreamURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	switch s.i.MediaFormat {
	case Video:
		return s.BuildVideoStreamURL(u, suffix)
	case Audio:
		return s.BuildAudioStreamURL(u, suffix)
	case Subtitle:
		return s.BuildSubtitleStreamURL(u)
	}
	return
}

func (s *TorrentStatURLBuilder) BuildStatURL(i *MyURL) *MyURL {
	u := i
	q := u.Query()
	if !i.cached {
		q.Add("stats", "true")
	} else {
		return &MyURL{}
	}
	u.RawQuery = q.Encode()
	return u
}

func (s *TorrentStatURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	if u.cached {
		return nil, nil
	}
	u = s.BuildStatURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	s.BuildScheme(u)
	return
}

func (s *SubtitlesURLBuilder) BuildSubtitlesURL(i *MyURL) (u *MyURL) {
	u = i
	u.Path += ServiceSeparator + string(ServiceTypeVideoInfo) + "/subtitles.json"

	q := u.Query()
	if s.g.Query("imdb-id") != "" {
		q.Add("imdb-id", s.g.Query("imdb-id"))
	}
	u.RawQuery = q.Encode()
	return u
}

func (s *SubtitlesURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u = s.BuildSubtitlesURL(u)
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	s.BuildScheme(u)
	return
}

func (s *MediaProbeURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildStreamURL(u, "/index.json")
	if err != nil {
		return
	}
	if !u.transcode {
		return nil, nil
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	s.BuildScheme(u)
	return
}
