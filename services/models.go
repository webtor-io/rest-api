package services

type ResourceResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MagnetURI string `json:"magnet_uri,omitempty"`
	// MultiFile is false for single-file-mode torrents (one file sitting at
	// the torrent root). Clients can render such a torrent straight away
	// without a /list round-trip.
	MultiFile bool `json:"multi_file"`
	// File is the single file of a single-file torrent (nil when MultiFile).
	// Lets clients skip /list for the common single-file case.
	File *ListItem `json:"file,omitempty"`
	// Size is the torrent's total size in bytes (sum of all files). Lets
	// clients read the size without paginating /list — a real win on torrents
	// with tens of thousands of files.
	Size int64 `json:"size"`
	// FilesCount is the number of files in the torrent.
	FilesCount int `json:"files_count"`
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
	// Index is the file's position in the torrent's natural file order
	// (r.Files), i.e. the content_id accepted by /resource/<hash>/export/<idx>.
	// Valid only for Type == file items; directory items leave it zero. Lets
	// clients address the file directly without re-deriving the index from
	// the sorted/paginated list order.
	Index int `json:"index"`
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
