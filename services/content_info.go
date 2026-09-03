package services

type MediaFormat string

const (
	Audio    MediaFormat = "audio"
	Video    MediaFormat = "video"
	Image    MediaFormat = "image"
	Subtitle MediaFormat = "subtitle"
	Unknown  MediaFormat = "unknown"
)

// formats maps a media class to the file extensions we are willing to play.
// An extension missing here yields Unknown, which makes the stream exporter
// return nothing and leaves the file with a download button only — the honest
// outcome for archives, executables and documents, but a defect for anything
// playable. Extensions are added only when we can actually serve them: a play
// button that leads to a transcoder error is worse than no play button.
var formats = map[MediaFormat][]string{
	// mov and m2ts carry H264 in practice, so they remux rather than
	// re-encode. Containers needing a full video re-encode (wmv/VC-1,
	// mpg/MPEG-2, rmvb/RV40) are deliberately left out — see transcodeExt.
	Video: {"avi", "mkv", "mp4", "webm", "m4v", "ts", "vob", "mov", "m2ts"},
	// m4b is the audiobook flavour of MP4 and the single largest gap here:
	// 1.33% of a 6000-torrent sample from torrent-store carried one, and for
	// 38 of those torrents it was the only playable file.
	Audio: {"mp3", "wav", "ogg", "flac", "m4a", "m4b", "aac", "opus", "wma", "ape", "mka", "wv"},
	// webp/avif have MIME types in Go's builtin table, so the browser gets a
	// correct Content-Type. bmp does not, but browsers sniff it fine; it just
	// never becomes a thumbnail, since web-ui registers no bmp decoder.
	Image: {"png", "gif", "jpg", "jpeg", "webp", "bmp", "avif"},
	// Text subtitles only. sub/idx (VobSub) and sup (PGS) are bitmap formats
	// srt2vtt cannot convert.
	Subtitle: {"srt", "vtt", "ass", "ssa"},
}

// transcodeExt lists the extensions routed through content-transcoder instead
// of being served as-is. For video that means everything nginx-vod cannot
// package. For audio it means everything the browser cannot be trusted to play
// from a bare file response: the runtime image ships no mime.types, so
// mime.TypeByExtension returns "" for every audio extension and the player has
// no Content-Type to go on. mp3/wav/ogg predate that reasoning and are left
// alone — they work today and moving them onto the transcoder would spend CPU
// to fix nothing.
var transcodeExt = []string{
	"avi", "mkv", "m4v", "ts", "vob", "mov", "m2ts",
	"flac", "m4a", "m4b", "aac", "opus", "wma", "ape", "mka", "wv",
}

func shouldTranscode(ext string) bool {
	for _, te := range transcodeExt {
		if te == ext {
			return true
		}
	}
	return false
}

func getMediaFormatByExt(ext string) MediaFormat {
	for f, e := range formats {
		for _, ee := range e {
			if ee == ext {
				return f
			}
		}
	}
	return Unknown
}
