package services

import (
	"crypto/sha1"
	"fmt"
	"net/url"
	"strings"

	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"

	"github.com/urfave/cli"
)

type MyURL struct {
	url.URL
	cached          bool
	transcode       bool
	multibitrate    bool
	transcodeCached bool
}

func (s *MyURL) BuildExportMeta() *ExportMeta {
	return &ExportMeta{
		Cache:          s.cached,
		Transcode:      s.transcode,
		TranscodeCache: s.transcodeCached,
	}
}

type URLBuilder struct {
	sd                *Subdomains
	cm                *CacheMap
	domain            string
	apiSecret         string
	apiKey            string
	apiRole           string
	useSubdomains     bool
	subdomainsK8SPool string
	pathPrefix        string
	premiumDomain     string
}

func NewURLBuilder(c *cli.Context, sd *Subdomains, cm *CacheMap) *URLBuilder {
	return &URLBuilder{
		sd:                sd,
		cm:                cm,
		domain:            c.String(exportDomainFlag),
		premiumDomain:     c.String(exportPremiumDomainFlag),
		apiKey:            c.String(exportApiKeyFlag),
		apiSecret:         c.String(exportApiSecretFlag),
		apiRole:           c.String(exportApiRoleFlag),
		useSubdomains:     c.BoolT(exportUseSubdomainsFlag),
		subdomainsK8SPool: c.String(exportSubdomainsK8SPoolFlag),
		pathPrefix:        c.String(exportPathPrefixFlag),
	}
}

func (s *URLBuilder) Build(r *Resource, i *ListItem, g ParamGetter, et ExportType) (*MyURL, error) {
	bubc := BaseURLBuilder{
		sd:                s.sd,
		cm:                s.cm,
		r:                 r,
		i:                 i,
		g:                 g,
		domain:            s.domain,
		premiumDomain:     s.premiumDomain,
		apiKey:            s.apiKey,
		apiSecret:         s.apiSecret,
		apiRole:           s.apiRole,
		useSubdomains:     s.useSubdomains,
		subdomainsK8SPool: s.subdomainsK8SPool,
		pathPrefix:        s.pathPrefix,
	}
	switch et {
	case ExportTypeDownload:
		dub := &DownloadURLBuilder{
			BaseURLBuilder: bubc,
		}
		return dub.Build()
	case ExportTypeStream:
		sub := &StreamURLBuilder{
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
				BaseURLBuilder: bubc,
			},
		}
		return sub.Build()
	}
	return nil, nil
}

type BaseURLBuilder struct {
	sd                *Subdomains
	cm                *CacheMap
	r                 *Resource
	i                 *ListItem
	g                 ParamGetter
	domain            string
	apiSecret         string
	apiKey            string
	apiRole           string
	subdomainsK8SPool string
	useSubdomains     bool
	pathPrefix        string
	premiumDomain     string
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

func (s *BaseURLBuilder) getApiKey() string {
	if s.g.Query("api-key") != "" {
		return s.g.Query("api-key")
	}
	if s.g.GetHeader("X-Api-Key") != "" {
		return s.g.GetHeader("X-Api-Key")
	}
	if s.apiKey != "" {
		return s.apiKey
	}
	return ""
}

func (s *BaseURLBuilder) getToken() (string, error) {
	if s.g.Query("token") != "" {
		return s.g.Query("token"), nil
	}
	if s.g.GetHeader("X-Token") != "" {
		return s.g.GetHeader("X-Token"), nil
	}
	if s.apiSecret != "" {
		claims := jwt.MapClaims{}
		if s.apiRole != "" {
			claims["role"] = s.apiRole
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tokenString, err := token.SignedString([]byte(s.apiSecret))
		if err != nil {
			return "", err
		}
		return tokenString, nil
	}
	return "", nil
}

func (s *BaseURLBuilder) getClaims() (jwt.MapClaims, error) {

	tokenString, err := s.getToken()

	if err != nil {
		return nil, err
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.Errorf("unexpected signing method=%v", token.Header["alg"])
		}
		return []byte(s.apiSecret), nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.Wrapf(err, "failed to validate token")
	}
	return claims, nil
}

func (s *BaseURLBuilder) getRole() (string, error) {
	claims, err := s.getClaims()
	if err != nil {
		return "", err
	}
	if r, ok := claims["role"].(string); ok {
		return r, nil
	}
	return "", nil
}

func (s *BaseURLBuilder) getUserID() string {
	if s.g.Query("user-id") != "" {
		return s.g.Query("user-id")
	}
	if s.g.GetHeader("X-User-Id") != "" {
		return s.g.GetHeader("X-User-Id")
	}
	return ""
}

func (s *BaseURLBuilder) getRequestID() string {
	if s.g.Query("request-id") != "" {
		return s.g.Query("request-id")
	}
	if s.g.GetHeader("X-Request-Id") != "" {
		return s.g.GetHeader("X-Request-Id")
	}
	return ""
}

func (s *BaseURLBuilder) BuildBaseURL(i *MyURL) (u *MyURL, err error) {
	u = i
	u.Path = s.pathPrefix + s.r.ID + "/" + strings.Trim(s.i.PathStr, "/")
	q := u.Query()
	apiKey := s.getApiKey()
	if apiKey != "" {
		q.Add("api-key", apiKey)
	}
	token, err := s.getToken()
	if err != nil {
		return nil, err
	}
	if token != "" {
		q.Add("token", token)
	}
	userID := s.getUserID()
	if userID != "" {
		q.Add("user-id", userID)
	}
	requestID := s.getRequestID()
	if requestID != "" {
		q.Add("request-id", requestID)
	}
	u.RawQuery = q.Encode()
	cached, err := s.cm.Get(u)
	if err != nil {
		return nil, err
	}
	u.cached = cached
	return
}

func (s *BaseURLBuilder) getBaseDomain() string {
	if s.domain == "" {
		return ""
	}
	if s.premiumDomain != "" {
		role, err := s.getRole()
		if err != nil {
			return ""
		}
		if role != "free" {
			return s.premiumDomain
		}
	}
	return s.domain
}

func (s *BaseURLBuilder) BuildScheme(i *MyURL) (u *MyURL, err error) {
	u = i
	domain := s.getBaseDomain()
	if domain == "" {
		u.Scheme = "http"
		return
	}
	du, err := url.Parse(domain)
	if err != nil {
		return nil, err
	}
	u.Scheme = du.Scheme
	return
}

func (s *BaseURLBuilder) BuildDomain(i *MyURL) (u *MyURL, err error) {
	u = i
	baseDomain := s.getBaseDomain()
	if baseDomain == "" {
		return u, nil
	}
	du, err := url.Parse(baseDomain)
	if err != nil {
		return nil, err
	}
	domain := du.Host
	if s.useSubdomains {
		pool := s.subdomainsK8SPool
		role, err := s.getRole()
		if err != nil {
			return nil, err
		}
		subs, err := s.sd.Get(s.r.ID, pool, role)
		if err != nil {
			return nil, err
		}
		if len(subs) > 0 {
			domain = subs[0] + "." + domain
		}
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

func (s *DownloadURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildScheme(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildDownloadURL(u)
	if err != nil {
		return
	}
	return
}

func (s *StreamURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildScheme(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u, err = s.BuildStreamURL(u, "/index.m3u8")
	if err != nil {
		return
	}
	return
}

func (s *StreamURLBuilder) BuildTranscodeURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	u.Path += ServiceSeparator + string(ServiceTypeTranscode) + suffix
	u.transcode = true
	cached, err := s.cm.Get(u)
	if err != nil {
		return nil, err
	}
	u.transcodeCached = cached
	return u, nil
}

func (s *StreamURLBuilder) BuildVODURL(i *MyURL, suffix string) (u *MyURL) {
	u = i
	u.Path += ServiceSeparator + string(ServiceTypeVOD) + "/hls/" + fmt.Sprintf("%x", sha1.Sum([]byte(s.r.ID+s.i.ID))) + suffix
	return u
}

func (s *StreamURLBuilder) BuildVideoStreamURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	if shouldTranscode(s.i.Ext) {
		u, err = s.BuildTranscodeURL(i, suffix)
		return
	} else {
		u = s.BuildVODURL(i, suffix)
	}
	return
}

func (s *StreamURLBuilder) BuildAudioStreamURL(i *MyURL, suffix string) (u *MyURL, err error) {
	u = i
	if shouldTranscode(s.i.Ext) {
		u, err = s.BuildTranscodeURL(i, suffix)
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
	u, err = s.BuildScheme(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	u, err = s.BuildBaseURL(u)
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

func (s *SubtitlesURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildScheme(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
	u, err = s.BuildBaseURL(u)
	if err != nil {
		return
	}
	u = s.BuildSubtitlesURL(u)
	return
}

func (s *MediaProbeURLBuilder) Build() (u *MyURL, err error) {
	u = &MyURL{}
	u, err = s.BuildScheme(u)
	if err != nil {
		return
	}
	u, err = s.BuildDomain(u)
	if err != nil {
		return
	}
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
	return
}
