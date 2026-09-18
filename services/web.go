package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/webtor-io/rest-api/docs"
)

// @title           Webtor API
// @version         0.1
// @description     Simple API to communicate with Webtor service.

// @contact.name   Webtor Support
// @contact.url    https://webtor.io/support
// @contact.email  support@webtor.io

// @securityDefinitions.apikey ApiKeyHeader
// @in   header
// @name X-Api-Key
// @description Optional API key. It is not validated by this API itself, it is embedded as api-key into the signed export/speedtest URLs and validated by the upstream services that serve them.

// @securityDefinitions.apikey ApiKeyQuery
// @in   query
// @name api-key
// @description Optional API key passed as a query parameter. Same semantics as X-Api-Key header (query value takes precedence).

const (
	webHostFlag = "host"
	webPortFlag = "port"
)

type Web struct {
	host string
	port int
	ln   net.Listener
	rm   *ResourceMap
	c    *List
	e    *Export
	st   *SpeedTest
}

func NewWeb(c *cli.Context, rm *ResourceMap, co *List, ex *Export, st *SpeedTest) *Web {
	return &Web{
		host: c.String(webHostFlag),
		port: c.Int(webPortFlag),
		rm:   rm,
		c:    co,
		e:    ex,
		st:   st,
	}
}

func RegisterWebFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   webHostFlag,
			Usage:  "listening host",
			Value:  "",
			EnvVar: "WEB_HOST",
		},
		cli.IntFlag{
			Name:   webPortFlag,
			Usage:  "http listening port",
			Value:  8080,
			EnvVar: "WEB_PORT",
		},
	)
}

// @Summary Stores resource
// @Description Receives torrent or magnet-uri in request body.
// @Description If magnet-uri provided instead of torrent, then it tries to fetch torrent from BitTorrent network (timeout 3 minutes).
// @Param resource body string true "resource" example("magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969&tr=udp%3A%2F%2Ftracker.coppersurfer.tk%3A6969&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337&tr=udp%3A%2F%2Fexplodie.org%3A6969&tr=udp%3A%2F%2Ftracker.empire-js.us%3A1337&tr=wss%3A%2F%2Ftracker.btorrent.xyz&tr=wss%3A%2F%2Ftracker.openwebtorrent.com&tr=wss%3A%2F%2Ftracker.fastcast.nz&ws=https%3A%2F%2Fwebtorrent.io%2Ftorrents%2F")
// @Schemes
// @Tags   resource
// @Accept */*
// @Produce json
// @Success 200 {object} ResourceResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 408 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /resource/ [post]
func (s *Web) postResource(g *gin.Context) {
	b := g.Request.Body
	defer b.Close()
	bb, err := io.ReadAll(b)
	if err != nil {
		g.AbortWithError(http.StatusInternalServerError, err)
		return
	}
	r, err := s.rm.Get(g.Request.Context(), bb)
	if err != nil {
		g.Error(err)
		return
	}
	rr := &ResourceResponse{
		ID:        r.ID,
		Name:      r.Name,
		MagnetURI: r.MagnetURI,
	}
	s.fillResourceStructure(rr, r)
	g.PureJSON(http.StatusOK, rr)
}

// @Summary Returns resource
// @Description Receives resource id and returns resource.
// @Schemes
// @Param resource_id path string true "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Tags  resource
// @Accept */*
// @Produce json
// @Success 200 {object} ResourceResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /resource/{resource_id} [get]
func (s *Web) getResource(g *gin.Context) {
	id := g.Param("resource_id")
	if strings.HasSuffix(id, ".torrent") {
		s.getTorrent(g)
		return
	}
	// Serve from the lightweight manifest (Files RPC) instead of pulling and
	// parsing the whole .torrent: this endpoint only needs id/name/structure.
	// The magnet is synthesized from the infohash — the torrent is already in
	// the store, so a trackerless magnet resolves fine for webtor.
	r, err := s.rm.GetManifest(g.Request.Context(), strings.ToLower(id))
	if err != nil {
		g.Error(err)
		return
	}
	rr := &ResourceResponse{
		ID:        r.ID,
		Name:      r.Name,
		MagnetURI: magnetFromInfoHash(r.ID, r.Name),
	}
	s.fillResourceStructure(rr, r)
	g.PureJSON(http.StatusOK, rr)
}

// fillResourceStructure sets MultiFile and, for single-file-mode torrents
// (one file at the torrent root), the single File item — so clients can
// render that common case without a /list round-trip.
func (s *Web) fillResourceStructure(rr *ResourceResponse, r *Resource) {
	rr.FilesCount = len(r.Files)
	for i := range r.Files {
		rr.Size += r.Files[i].Size
	}
	if len(r.Files) == 1 && len(r.Files[0].Path) == 1 {
		it := s.c.buildFile(r.Files[0], 0)
		rr.File = &it
		return
	}
	rr.MultiFile = true
}

// magnetFromInfoHash builds a minimal magnet URI from an infohash (and
// optional display name). The announce list is intentionally omitted —
// webtor resolves by infohash against the store, so listing/embed paths
// don't need trackers. xt stays first so the prefix matches the configured
// demo magnet (demo detection relies on a prefix match).
func magnetFromInfoHash(id, name string) string {
	m := "magnet:?xt=urn:btih:" + id
	if name != "" {
		m += "&dn=" + url.QueryEscape(name)
	}
	return m
}

// @Summary Returns torrent for resource
// @Description Receives id and returns torrent file (binary, Content-Type application/x-bittorrent) for resource.
// @Schemes
// @Param resource_id path string true "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Tags  resource
// @Accept */*
// @Produce application/x-bittorrent
// @Success 200 {file} binary "torrent file"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /resource/{resource_id}.torrent [get]
func (s *Web) getTorrent(g *gin.Context) {
	id := g.Param("resource_id")
	id = strings.TrimSuffix(id, ".torrent")
	r, err := s.rm.Get(g.Request.Context(), []byte(id))
	if err != nil {
		g.Error(err)
		return
	}
	g.Data(http.StatusOK, "application/x-bittorrent", r.Torrent)
}

// @Summary Lists resource
// @Description Lists files and directories of specific resource.
// @Description All ids in response can be used for export.
// @Param resource_id path  string true  "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Param path        query string false "path"
// @Param limit       query int    false "limit"
// @Param offset      query int    false "offset"
// @Param output      query string false "output" Enums(list, tree)
// @Param sort        query string false "sort order; if omitted, items are returned in the torrent's original file order" Enums(name, size)
// @Schemes
// @Tags   list
// @Accept */*
// @Produce json
// @Success 200 {object} ListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /resource/{resource_id}/list [get]
func (s *Web) getList(g *gin.Context) {
	args, err := ListGetArgsFromParams(g)
	if err != nil {
		g.Error(err)
		return
	}
	id := strings.ToLower(g.Param("resource_id"))
	r, err := s.rm.GetManifest(g.Request.Context(), id)
	if err != nil {
		g.Error(err)
		return
	}
	args.Ctx = g.Request.Context()
	cr, err := s.c.Get(r, args)
	if err != nil {
		g.Error(err)
		return
	}
	g.PureJSON(http.StatusOK, cr)
}

// @Summary Exports resource content
// @Description Provides url for exporting resource content. content_id is
// @Description either the SHA1 of the file's path (returned by /list) or
// @Description the file's index in the torrent's natural file order
// @Description (matches the fileIdx convention used by Stremio addons).
// @Param resource_id path  string true  "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Param content_id  path  string true  "content_id"  example("ca2453df3e7691c28934eebed5a253ee0aabd29f")
// @Param types query string false "comma-separated list of export types to generate; allowed values: download, stream, torrent_client_stat, subtitles, media_probe; if omitted or empty, all types are exported; an unknown value yields 400" example("download,stream")
// @Param api-key query string false "API key embedded into the generated export URLs; falls back to X-Api-Key header, then to the server-configured key"
// @Param X-Api-Key header string false "API key embedded into the generated export URLs (used if api-key query parameter is absent)"
// @Param token query string false "JWT embedded into the generated export URLs; falls back to X-Token header; if neither is provided, the server signs its own token with the configured role; the token's role claim selects the premium domain and download subdomains"
// @Param X-Token header string false "JWT embedded into the generated export URLs (used if token query parameter is absent)"
// @Param user-id query string false "opaque user identifier propagated into the generated export URLs; falls back to X-User-Id header"
// @Param X-User-Id header string false "opaque user identifier propagated into the generated export URLs (used if user-id query parameter is absent)"
// @Param request-id query string false "request correlation identifier propagated into the generated export URLs; falls back to X-Request-Id header"
// @Param X-Request-Id header string false "request correlation identifier propagated into the generated export URLs (used if request-id query parameter is absent)"
// @Param use-premium-domain query string false "any value except \"false\" (including omission) allows premium-role tokens to get URLs on the premium domain; set to \"false\" to force the standard domain; torrent_client_stat URLs always use the standard domain" default(true)
// @Param imdb-id query string false "IMDB identifier forwarded into the subtitles export URL (subtitles type only)"
// @Param archive-format query string false "archive format for directory downloads" Enums(zip, tar) default(zip)
// @Param paths query []string false "limit a directory archive to the selected file/folder paths (repeatable); each path must exist in the torrent and be inside the exported directory; at most 1024 paths and about 6000 percent-encoded bytes in total" collectionFormat(multi)
// @Schemes
// @Tags export
// @Accept */*
// @Produce json
// @Success 200 {object} ExportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /resource/{resource_id}/export/{content_id} [get]
func (s *Web) getExport(g *gin.Context) {
	args, err := ExportGetArgsFromParams(g)
	if err != nil {
		g.Error(err)
		return
	}
	contentID := strings.ToLower(g.Param("content_id"))
	resourceID := strings.ToLower(g.Param("resource_id"))
	// The manifest (ID, Name, Files) is all export needs; Get pulled and
	// parsed the whole .torrent — 21 MB for a 184k-file dump, over the 10 s
	// store timeout, which is where the 504s on export came from.
	r, err := s.rm.GetManifest(g.Request.Context(), resourceID)
	if err != nil {
		g.Error(err)
		return
	}

	var item *ListItem
	if idx, ierr := strconv.Atoi(contentID); ierr == nil {
		// content_id is a file index into the torrent's natural file order.
		// Lets clients (Stremio addon) skip the /list round-trip when they
		// already know which file in the torrent they want.
		if idx < 0 || idx >= len(r.Files) {
			g.Error(errors.Errorf("file idx %d out of range (resource has %d files)", idx, len(r.Files)))
			return
		}
		it := s.c.buildFile(r.Files[idx], idx)
		item = &it
	} else if sha1R.Match([]byte(contentID)) {
		if it, ok := s.c.FindByID(r, contentID); ok {
			item = it
		}
	} else {
		g.Error(errors.Errorf("failed to parse content id %v", contentID))
		return
	}

	if item == nil {
		g.Error(errors.Errorf("content with id %v not found", contentID))
		return
	}
	res, err := s.e.Get(r, item, args, g)
	if err != nil {
		g.Error(err)
		return
	}
	g.PureJSON(http.StatusOK, res)
}

func (s *Web) errorHandler(c *gin.Context) {
	c.Next()
	if len(c.Errors) == 0 {
		return
	}
	err := c.Errors[0]
	log.Error(err)

	status := http.StatusInternalServerError

	if strings.Contains(err.Error(), "failed to parse") {
		status = http.StatusBadRequest
	} else if strings.Contains(err.Error(), "forbidden") {
		status = http.StatusForbidden
	} else if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
	} else if strings.Contains(err.Error(), "timeout") {
		status = http.StatusRequestTimeout
	} else if strings.Contains(err.Error(), "deadline exceeded") {
		// Upstream ran out of time, not the client — 504, so consumers
		// (RapidAPI et al.) can tell a transient dependency stall from a bug
		// on our side and retry instead of filing a bug report.
		status = http.StatusGatewayTimeout
	}
	c.PureJSON(status, &ErrorResponse{Error: err.Error()})
}

func (s *Web) Serve() error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	ln, err := net.Listen("tcp", addr)
	s.ln = ln
	if err != nil {
		return errors.Wrap(err, "Failed to web listen to tcp connection")
	}
	r := gin.Default()
	r.UseRawPath = true
	r.Use(s.errorHandler)
	rg := r.Group("/resource")
	{
		rg.POST("/", s.postResource)
		rg.GET("/:resource_id", s.getResource)
		rg.GET("/:resource_id/list", s.getList)
		rg.GET("/:resource_id/export/:content_id", s.getExport)
	}
	if s.st != nil {
		r.GET("/speedtest", s.getSpeedtest)
	}
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	docs.SwaggerInfo.BasePath = "/"
	log.Infof("serving Web at %v", addr)
	return http.Serve(s.ln, r)
}

// @Summary Returns speed test urls
// @Description Returns urls for measuring download speed. Each url points to a /speedtest endpoint on a download host and carries the size, token and api-key query parameters.
// @Description The "standard" url is always present; a "premium" url is added when a premium domain is configured. The token's role claim selects which download subdomains are eligible.
// @Param token query string false "JWT embedded into the generated speed test URLs; falls back to X-Token header; if neither is provided, the server signs its own token with the configured role"
// @Param X-Token header string false "JWT embedded into the generated speed test URLs (used if token query parameter is absent)"
// @Param api-key query string false "API key embedded into the generated speed test URLs; falls back to X-Api-Key header, then to the server-configured key"
// @Param X-Api-Key header string false "API key embedded into the generated speed test URLs (used if api-key query parameter is absent)"
// @Schemes
// @Tags speedtest
// @Accept */*
// @Produce json
// @Success 200 {object} SpeedtestResponse
// @Failure 500 {object} ErrorResponse
// @Router /speedtest [get]
func (s *Web) getSpeedtest(g *gin.Context) {
	urls, err := s.st.GetURLs(g)
	if err != nil {
		g.Error(err)
		return
	}
	g.PureJSON(http.StatusOK, &SpeedtestResponse{URLs: urls})
}

func (s *Web) Close() {
	log.Info("closing Web")
	defer func() {
		log.Info("Web closed")
	}()
	if s.ln != nil {
		_ = s.ln.Close()
	}
}
