package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
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
}

func NewWeb(c *cli.Context, rm *ResourceMap, co *List, ex *Export) *Web {
	return &Web{
		host: c.String(webHostFlag),
		port: c.Int(webPortFlag),
		rm:   rm,
		c:    co,
		e:    ex,
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
	g.PureJSON(http.StatusOK, &ResourceResponse{
		ID:        r.ID,
		Name:      r.Name,
		MagnetURI: r.MagnetURI,
	})
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
	r, err := s.rm.Get(g.Request.Context(), []byte(id))
	if err != nil {
		g.Error(err)
		return
	}
	g.PureJSON(http.StatusOK, &ResourceResponse{
		ID:        r.ID,
		Name:      r.Name,
		MagnetURI: r.MagnetURI,
	})
}

// @Summary Returns torrent for resource
// @Description Receives id and returns torrent for resource.
// @Schemes
// @Param resource_id path string true "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Tags  resource
// @Accept */*
// @Produce application/x-bittorrent
// @Success 200 {object} ResourceResponse
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
	id := g.Param("resource_id")
	r, err := s.rm.Get(g.Request.Context(), []byte(id))
	if err != nil {
		g.Error(err)
		return
	}
	cr, err := s.c.Get(r, args)
	if err != nil {
		g.Error(err)
		return
	}
	g.PureJSON(http.StatusOK, cr)
}

// @Summary Exports resource content
// @Description Provides url for exporting resource content.
// @Param output      query string false "output"      Enums(download, stream, torrent_client_stat, subtitles, media_probe)
// @Param resource_id path  string true  "resource_id" example("08ada5a7a6183aae1e09d831df6748d566095a10")
// @Param content_id  path  string true  "content_id"  example("ca2453df3e7691c28934eebed5a253ee0aabd29f")
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
	contentID := g.Param("content_id")
	if !sha1R.Match([]byte(contentID)) {
		g.Error(errors.Errorf("failed to parse content id %v", contentID))
		return
	}
	resourceID := g.Param("resource_id")
	r, err := s.rm.Get(g.Request.Context(), []byte(resourceID))
	if err != nil {
		g.Error(err)
		return
	}
	cr, err := s.c.Get(r, NewListGetArgs())
	if err != nil {
		g.Error(err)
		return
	}
	var item *ListItem
	for _, i := range cr.Items {
		if i.ID == contentID {
			item = &i
			break
		}
	}
	if item == nil && cr.ID == contentID {
		item = &cr.ListItem
	}
	if item == nil {
		g.Error(errors.Errorf("content with id %v not found", contentID))
		return
	}
	res, err := s.e.Get(g.Request.Context(), r, item, args, g)
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
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	docs.SwaggerInfo.BasePath = "/"
	log.Infof("serving Web at %v", addr)
	return http.Serve(s.ln, r)
}

func (s *Web) Close() {
	log.Info("closing Web")
	defer func() {
		log.Info("Web closed")
	}()
	if s.ln != nil {
		s.ln.Close()
	}
}
