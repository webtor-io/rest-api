package services

import (
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const (
	exportDomainFlag = "export-domain"
	exportSSLFlag    = "export-ssl"
)

func RegisterExportFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   exportDomainFlag,
			Usage:  "export domain",
			Value:  "",
			EnvVar: "EXPORT_DOMAIN",
		},
		cli.BoolTFlag{
			Name:   exportSSLFlag,
			Usage:  "export ssl",
			EnvVar: "EXPORT_SSL",
		},
	)
}

type ExportType string

const (
	ExportTypeDownload    ExportType = "download"
	ExportTypeStream      ExportType = "stream"
	ExportTypeTorrentStat ExportType = "torrent_client_stat"
	ExportTypeSubtitles   ExportType = "subtitles"
	ExportTypeMediaProbe  ExportType = "media_probe"
)

var ExportTypes = []ExportType{
	ExportTypeDownload,
	ExportTypeStream,
	ExportTypeTorrentStat,
	ExportTypeSubtitles,
	ExportTypeMediaProbe,
}

type ExportGetArgs struct {
	Types []ExportType
}

type Export struct {
	exporters []Exporter
}

func ExportGetArgsFromParams(g ParamGetter) (*ExportGetArgs, error) {
	types := []ExportType{}
	if g.Query("types") != "" {
		for _, k := range strings.Split(g.Query("types"), ",") {
			kk := strings.TrimSpace(k)
			found := false
			for _, t := range ExportTypes {
				if string(t) == kk {
					types = append(types, t)
					found = true
					break
				}
			}
			if !found {
				return nil, errors.Errorf("failed to parse export type \"%v\"", kk)
			}
		}
	} else {
		types = ExportTypes
	}
	return &ExportGetArgs{
		Types: types,
	}, nil
}

type Exporter interface {
	Type() ExportType
	Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error)
}

func NewExport(e ...Exporter) *Export {
	return &Export{
		exporters: e,
	}
}

func (s *Export) Get(r *Resource, i *ListItem, args *ExportGetArgs, g ParamGetter) (*ExportResponse, error) {
	items := map[string]ExportItem{}
	for _, t := range args.Types {
		for _, e := range s.exporters {
			if e.Type() == t {
				ex, err := e.Export(r, i, g)
				if err != nil {
					return nil, err
				}
				if ex != nil {
					items[ex.Type] = *ex
				}
			}
		}
	}
	return &ExportResponse{
		Source:      *i,
		ExportItems: items,
	}, nil
}

type BaseExporter struct {
	ub         *URLBuilder
	exportType ExportType
}

func (s *BaseExporter) Type() ExportType {
	return s.exportType
}

func (s *BaseExporter) BuildURL(r *Resource, i *ListItem, g ParamGetter) (*MyURL, error) {
	return s.ub.Build(r, i, g, s.Type())
}

type DownloadExporter struct {
	BaseExporter
}

type StreamExporter struct {
	tb *TagBuilder
	BaseExporter
}

type MediaProbeExporter struct {
	BaseExporter
}

type TorrentStatExporter struct {
	BaseExporter
}

type SubtitlesExporter struct {
	BaseExporter
}

func NewDownloadExporter(ub *URLBuilder) *DownloadExporter {
	return &DownloadExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeDownload,
		},
	}
}

func (s *DownloadExporter) Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(r, i, g)
	if err != nil {
		return nil, err
	}

	if err != nil {
		return nil, err
	}
	return &ExportItem{
		Type: string(s.Type()),
		URL:  url.String(),
		ExportMetaItem: ExportMetaItem{
			Meta: url.BuildExportMeta(),
		},
	}, nil
}

func NewStreamExporter(ub *URLBuilder, tb *TagBuilder) *StreamExporter {
	return &StreamExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeStream,
		},
		tb: tb,
	}
}

func (s *StreamExporter) Type() ExportType {
	return ExportTypeStream
}

func (s *StreamExporter) MakeExportStreamItem(r *Resource, i *ListItem, g ParamGetter) (*ExportStreamItem, error) {
	ei := &ExportStreamItem{}
	t, err := s.tb.Build(r, i, g)
	if err != nil {
		return nil, err
	}
	if t != nil {
		ei.Tag = t
	}
	return ei, nil
}

func (s *StreamExporter) Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	if i.MediaFormat == "" {
		return nil, nil
	}
	url, err := s.BuildURL(r, i, g)
	if err != nil {
		return nil, err
	}

	ei, err := s.MakeExportStreamItem(r, i, g)
	if err != nil {
		return nil, err
	}

	return &ExportItem{
		Type:             string(s.Type()),
		URL:              url.String(),
		ExportStreamItem: *ei,
		ExportMetaItem: ExportMetaItem{
			Meta: url.BuildExportMeta(),
		},
	}, nil
}

func NewTorrentStatExporter(ub *URLBuilder) *TorrentStatExporter {
	return &TorrentStatExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeTorrentStat,
		},
	}
}

func (s *TorrentStatExporter) Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(r, i, g)
	if err != nil {
		return nil, err
	}
	if url == nil {
		return nil, nil
	}

	return &ExportItem{
		Type: string(s.Type()),
		URL:  url.String(),
	}, nil
}

func NewSubtitlesExporter(ub *URLBuilder) *SubtitlesExporter {
	return &SubtitlesExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeSubtitles,
		},
	}
}

func (s *SubtitlesExporter) Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	if i.MediaFormat != Video {
		return nil, nil
	}
	url, err := s.BuildURL(r, i, g)
	if err != nil {
		return nil, err
	}
	if url == nil {
		return nil, nil
	}
	return &ExportItem{
		Type: string(s.Type()),
		URL:  url.String(),
	}, nil
}

func NewMediaProbeExporter(ub *URLBuilder) *MediaProbeExporter {
	return &MediaProbeExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeMediaProbe,
		},
	}
}

func (s *MediaProbeExporter) Export(r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(r, i, g)
	if err != nil {
		return nil, err
	}
	if url == nil {
		return nil, nil
	}
	return &ExportItem{
		Type: string(s.Type()),
		URL:  url.String(),
	}, nil
}
