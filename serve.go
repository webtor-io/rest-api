package main

import (
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	s "github.com/webtor-io/rest-api/services"
	"net/http"
)

func makeServeCMD() cli.Command {
	serveCmd := cli.Command{
		Name:    "serve",
		Aliases: []string{"s"},
		Usage:   "Serves web server",
		Action:  serve,
	}
	configureServe(&serveCmd)
	return serveCmd
}

func configureServe(c *cli.Command) {
	c.Flags = cs.RegisterProbeFlags(c.Flags)
	c.Flags = cs.RegisterPprofFlags(c.Flags)
	c.Flags = s.RegisterWebFlags(c.Flags)
	c.Flags = s.RegisterTorrentStoreFlags(c.Flags)
	c.Flags = s.RegisterMagnet2TorrentFlags(c.Flags)
	c.Flags = s.RegisterExportFlags(c.Flags)
	c.Flags = s.RegisterNodesStatFlags(c.Flags)
	c.Flags = s.RegisterPromClientFlags(c.Flags)
}

func serve(c *cli.Context) error {

	services := []cs.Servable{}

	// Setting Probe
	probe := cs.NewProbe(c)
	if probe != nil {
		services = append(services, probe)
		defer probe.Close()
	}

	// Setting PPROF
	pprof := cs.NewPprof(c)
	if pprof != nil {
		services = append(services, pprof)
		defer pprof.Close()
	}

	// Setting TorrentStore
	ts := s.NewTorrentStore(c)
	defer ts.Close()

	// Setting HTTP Client
	httpCl := http.DefaultClient

	// Seeting CacheMap
	cm := s.NewCacheMap(httpCl)

	// Setting Magnet2Torrent
	m2t := s.NewMagnet2Torrent(c)
	defer m2t.Close()

	// Setting ResourceMap
	rm := s.NewResourceMap(ts, m2t)

	// Setting List
	li := s.NewList()

	// Setting PromClient
	pcl := s.NewPromClient(c)

	// Setting K8SClient
	kcl := s.NewK8SClient()

	// Setting NodeStat
	ns := s.NewNodesStat(c, pcl, kcl)

	// Setting Subdomains
	sd := s.NewSubdomains(c, ns)

	// Setting URLBuilder
	ub := s.NewURLBuilder(c, sd, cm)

	// Setting DownloadExporter
	de := s.NewDownloadExporter(ub)

	tb := s.NewTagBuilder(ub, li)

	// Setting StreamExporter
	se := s.NewStreamExporter(ub, tb)

	// Setting TorrentStatExporter
	tse := s.NewTorrentStatExporter(ub)

	// Setting SubtitlesExporter
	vie := s.NewSubtitlesExporter(ub)

	// Setting MediaProbeExporter
	mpe := s.NewMediaProbeExporter(ub)

	// Setting Export
	ex := s.NewExport(de, se, tse, vie, mpe)

	// Setting Web
	web := s.NewWeb(c, rm, li, ex)
	if web != nil {
		services = append(services, web)
		defer web.Close()
	}

	// Setting Serve
	serve := cs.NewServe(services...)

	// And SERVE!
	err := serve.Serve()
	if err != nil {
		log.WithError(err).Error("got server error")
	}
	return err
}
