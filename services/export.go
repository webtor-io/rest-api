package services

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const (
	exportDomainFlag            = "export-domain"
	exportUseSubdomainsFlag     = "export-use-subdomains"
	exportSubdomainsK8SPoolFlag = "export-subdomains-k8s-pool"
	exportApiKeyFlag            = "export-api-key"
	exportApiSecretFlag         = "export-api-secret"
	exportApiRoleFlag           = "export-api-role"
	exportPathPrefixFlag        = "export-path-prefix"
)

const (
	videoInfoServiceHostFlag = "video-info-host"
	videoInfoServicePortFlag = "video-info-port"
)

func RegisterVideoInfoServiceFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   videoInfoServiceHostFlag,
			Usage:  "video info service host",
			EnvVar: "VIDEO_INFO_SERVICE_HOST",
			Value:  "",
		},
		cli.IntFlag{
			Name:   videoInfoServicePortFlag,
			Usage:  "video info service port",
			EnvVar: "VIDEO_INFO_SERVICE_PORT",
			Value:  0,
		},
	)
}

func RegisterExportFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   exportDomainFlag,
			Usage:  "export domain",
			Value:  "",
			EnvVar: "EXPORT_DOMAIN",
		},
		cli.StringFlag{
			Name:   exportApiKeyFlag,
			Usage:  "export api key",
			Value:  "",
			EnvVar: "EXPORT_API_KEY",
		},
		cli.StringFlag{
			Name:   exportApiSecretFlag,
			Usage:  "export api token",
			Value:  "",
			EnvVar: "EXPORT_API_SECRET",
		},
		cli.StringFlag{
			Name:   exportApiRoleFlag,
			Usage:  "export api role",
			Value:  "free",
			EnvVar: "EXPORT_API_ROLE",
		},
		cli.BoolTFlag{
			Name:   exportUseSubdomainsFlag,
			Usage:  "export use subdomains",
			EnvVar: "EXPORT_USE_SUBDOMAINS",
		},
		cli.StringFlag{
			Name:   exportSubdomainsK8SPoolFlag,
			Usage:  "export k8s pool",
			EnvVar: "EXPORT_K8S_POOL",
			Value:  "seeder",
		},
		cli.StringFlag{
			Name:   exportPathPrefixFlag,
			Usage:  "export path prefix",
			EnvVar: "EXPORT_PATH_PREFIX",
			Value:  "/",
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
	var types []ExportType
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
	Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error)
}

func NewExport(e ...Exporter) *Export {
	return &Export{
		exporters: e,
	}
}

func (s *Export) Get(ctx context.Context, r *Resource, i *ListItem, args *ExportGetArgs, g ParamGetter) (*ExportResponse, error) {
	items := map[string]ExportItem{}
	for _, t := range args.Types {
		for _, e := range s.exporters {
			if e.Type() == t {
				ex, err := e.Export(ctx, r, i, g)
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

func (s *BaseExporter) BuildURL(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*MyURL, error) {
	return s.ub.Build(ctx, r, i, g, s.Type())
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

func (s *DownloadExporter) Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(ctx, r, i, g)
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

func (s *StreamExporter) MakeExportStreamItem(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportStreamItem, error) {
	ei := &ExportStreamItem{}
	t, err := s.tb.Build(ctx, r, i, g)
	if err != nil {
		return nil, err
	}
	if t != nil {
		ei.Tag = t
	}
	return ei, nil
}

func (s *StreamExporter) Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	if i.MediaFormat == "" {
		return nil, nil
	}
	url, err := s.BuildURL(ctx, r, i, g)
	if err != nil {
		return nil, err
	}

	ei, err := s.MakeExportStreamItem(ctx, r, i, g)
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

func (s *TorrentStatExporter) Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(ctx, r, i, g)
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

func NewSubtitlesExporter(c *cli.Context, ub *URLBuilder) *SubtitlesExporter {
	if c.String(videoInfoServiceHostFlag) == "" && c.Int(videoInfoServicePortFlag) == 0 {
		return nil
	}
	return &SubtitlesExporter{
		BaseExporter: BaseExporter{
			ub:         ub,
			exportType: ExportTypeSubtitles,
		},
	}
}

func (s *SubtitlesExporter) Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	if i.MediaFormat != Video {
		return nil, nil
	}
	url, err := s.BuildURL(ctx, r, i, g)
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

func (s *MediaProbeExporter) Export(ctx context.Context, r *Resource, i *ListItem, g ParamGetter) (*ExportItem, error) {
	url, err := s.BuildURL(ctx, r, i, g)
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
