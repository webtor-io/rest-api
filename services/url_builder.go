package services

import (
	"context"
	"crypto/sha1"
	"fmt"
	"github.com/dgrijalva/jwt-go"
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
		Cache:     s.cached,
		Transcode: s.transcode,
	}
}

type URLBuilder struct {
	sd        *Subdomains
	cm        *CacheMap
	domain    string
	ssl       bool
	apiSecret string
	apiKey    string
}

func NewURLBuilder(c *cli.Context, sd *Subdomains, cm *CacheMap) *URLBuilder {
	return &URLBuilder{
		sd:        sd,
		cm:        cm,
		domain:    c.String(exportDomainFlag),
		ssl:       c.BoolT(exportSSLFlag),
		apiKey:    c.String(exportApiKeyFlag),
		apiSecret: c.String(exportApiSecretFlag),
	}
}

func (s *URLBuilder) Build(ctx context.Context, r *Resource, i *ListItem, g ParamGetter, et ExportType) (*MyURL, error) {
	bubc := BaseURLBuilder{
		sd:        s.sd,
		cm:        s.cm,
		r:         r,
		i:         i,
		g:         g,
		domain:    s.domain,
		ssl:       s.ssl,
		apiKey:    s.apiKey,
		apiSecret: s.apiSecret,
	}
	switch et {
	case ExportTypeDownload:
		dub := &DownloadURLBuilder{
			BaseURLBuilder: bubc,
		}
		return dub.Build(ctx)
	case ExportTypeStream:
		sub := &StreamURLBuilder{
			BaseURLBuilder: bubc,
		}
		return sub.Build(ctx)
	case ExportTypeTorrentStat:
		sub := &TorrentStatURLBuilder{
			BaseURLBuilder: bubc,
		}
		return sub.Build(ctx)
	case ExportTypeSubtitles:
		sub := &SubtitlesURLBuilder{
			BaseURLBuilder: bubc,
		}
		return sub.Build(ctx)
	case ExportTypeMediaProbe:
		sub := &MediaProbeURLBuilder{
			StreamURLBuilder: StreamURLBuilder{
				BaseURLBuilder: bubc,
			},
		}
		return sub.Build(ctx)
	}
	return nil, nil
}

type BaseURLBuilder struct {
	sd        *Subdomains
	cm        *CacheMap
	r         *Resource
	i         *ListItem
	g         ParamGetter
	domain    string
	ssl       bool
	apiSecret string
	apiKey    string
}

type DownloadURLBuilder struct {
	BaseURLBuilder
}

type StreamURLBuilder struct {
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
	ServiceTypeArchive   ServiceType = "arch"
	ServiceTypeTranscode ServiceType = "hls"
	ServiceTypeVOD       ServiceType = "vod"
	ServiceTypeSRT2VTT   ServiceType = "vtt"
	ServiceTypeVideoInfo ServiceType = "vi"
)

const ServiceSeparator = "~"

func (s *BaseURLBuilder) BuildBaseURL(ctx context.Context, i *MyURL) (u *MyURL, err error) {
	u = i
	if len(s.r.Files) > 1 {
		u.Path = "/" + s.r.ID + "/" + strings.TrimRight(s.r.Name+s.i.PathStr, "/")
	} else {
		u.Path = "/" + s.r.ID + "/" + strings.Trim(s.i.PathStr, "/")
	}
	q := u.Query()
	if s.g.Query("api-key") != "" {
		q.Add("api-key", s.g.Query("api-key"))
	} else if s.g.GetHeader("X-Api-Key") != "" {
		q.Add("api-key", s.g.GetHeader("X-Api-Key"))
	} else if s.apiKey != "" {
		q.Add("api-key", s.apiKey)
	}
	if s.g.Query("token") != "" {
		q.Add("token", s.g.Query("token"))
	} else if s.g.GetHeader("X-Token") != "" {
		q.Add("token", s.g.GetHeader("X-Token"))
	} else if s.apiSecret != "" {
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{})
		tokenString, err := token.SignedString([]byte(s.apiSecret))
		if err != nil {
			return nil, err
		}
		q.Add("token", tokenString)
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
	u.RawQuery = q.Encode()
	cached, err := s.cm.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	u.cached = cached
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

func (s *BaseURLBuilder) BuildDomain(ctx context.Context, i *MyURL) (u *MyURL, err error) {
	u = i
	if s.domain == "" {
		return u, nil
	}
	domain := s.domain
	pool := "seeder"
	subs, err := s.sd.Get(ctx, s.r.ID, pool)
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
	q := u.Query()
	q.Add("download", "true")
	u.RawQuery = q.Encode()
	return
}

func (s *DownloadURLBuilder) Build(ctx context.Context) (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildDomain(ctx, u)
	if err != nil {
		return
	}
	u, err = s.BuildBaseURL(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildScheme(u)
	u, err = s.BuildDownloadURL(u)
	if err != nil {
		return
	}
	return
}

func (s *StreamURLBuilder) Build(ctx context.Context) (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildDomain(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildScheme(u)
	u, err = s.BuildBaseURL(ctx, u)
	if err != nil {
		return
	}
	u, err = s.BuildStreamURL(u, "/index.m3u8")
	if err != nil {
		return
	}
	return
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

func (s *TorrentStatURLBuilder) Build(ctx context.Context) (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildDomain(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildScheme(u)
	u, err = s.BuildBaseURL(ctx, u)
	if err != nil {
		return
	}
	if u.cached {
		return nil, nil
	}
	u = s.BuildStatURL(u)
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

func (s *SubtitlesURLBuilder) Build(ctx context.Context) (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildDomain(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildScheme(u)
	u, err = s.BuildBaseURL(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildSubtitlesURL(u)
	return
}

func (s *MediaProbeURLBuilder) Build(ctx context.Context) (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildDomain(ctx, u)
	if err != nil {
		return
	}
	u = s.BuildScheme(u)
	u, err = s.BuildBaseURL(ctx, u)
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
	return
}
