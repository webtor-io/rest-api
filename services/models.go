package services

type ResourceResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MagnetURI string `json:"magnet_uri,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type ListType string

const (
	ListTypeFile      ListType = "file"
	ListTypeDirectory ListType = "directory"
)

type ListItem struct {
	ID          string      `json:"id"`
	Name        string      `json:"name,omitempty"`
	PathStr     string      `json:"path"`
	Path        []string    `json:"-"`
	Type        ListType    `json:"type"`
	Size        int64       `json:"size"`
	MediaFormat MediaFormat `json:"media_format,omitempty"`
	MimeType    string      `json:"mime_type,omitempty"`
	Ext         string      `json:"ext,omitempty"`
}

type ListResponse struct {
	ListItem
	Items []ListItem `json:"items"`
	Count int        `json:"items_count"`
}

type ExportItem struct {
	ExportStreamItem
	ExportMetaItem
	Type string `json:"-"`
	URL  string `json:"url,omitempty"`
}

type ExportSource struct {
	Src  string `json:"src"`
	Type string `json:"type"`
}

type ExportTrack struct {
	Src     string         `json:"src"`
	Kind    ExportKindType `json:"kind"`
	SrcLang string         `json:"srclang,omitempty"`
	Label   string         `json:"label,omitempty"`
}

type ExportPreloadType string

const (
	ExportPreloadTypeAuto ExportPreloadType = "auto"
	ExportPreloadTypeNone ExportPreloadType = "none"
)

type ExportKindType string

const (
	ExportKindTypeSubtitles ExportKindType = "subtitles"
)

type ExportTagName string

const (
	ExportTagNameVideo ExportTagName = "video"
	ExportTagNameAudio ExportTagName = "audio"
	ExportTagNameImage ExportTagName = "img"
)

type ExportTag struct {
	Name    ExportTagName     `json:"tag,omitempty"`
	Preload ExportPreloadType `json:"preload,omitempty"`
	Sources []ExportSource    `json:"sources,omitempty"`
	Tracks  []ExportTrack     `json:"tracks,omitempty"`
	Src     string            `json:"src,omitempty"`
	Alt     string            `json:"alt,omitempty"`
	Poster  string            `json:"poster,omitempty"`
}

type ExportStreamItem struct {
	Tag *ExportTag `json:"html_tag,omitempty"`
}

type ExportMetaItem struct {
	Meta *ExportMeta `json:"meta,omitempty"`
}

type ExportMeta struct {
	Transcode      bool `json:"transcode,omitempty"`
	Multibitrate   bool `json:"multibitrate,omitempty"`
	Cache          bool `json:"cache,omitempty"`
	TranscodeCache bool `json:"transcode_cache,omitempty"`
}

type ExportResponse struct {
	Source      ListItem              `json:"source"`
	ExportItems map[string]ExportItem `json:"exports"`
}

type Subtitles struct {
	URL string `json:"url"`
}
