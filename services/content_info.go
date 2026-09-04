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
	// All three are in Go's builtin MIME table, so the browser gets a correct
	// Content-Type without a system mime.types file. bmp still never becomes a
	// thumbnail, though — web-ui registers no bmp decoder.
	Image: {"png", "gif", "jpg", "jpeg", "webp", "bmp", "avif"},
	// Text subtitles only. sub/idx (VobSub) and sup (PGS) are bitmap formats
	// srt2vtt cannot convert.
	Subtitle: {"srt", "vtt", "ass", "ssa"},
}

// transcodeExt lists the extensions routed through content-transcoder instead
// of being served as-is. For video that means everything nginx-vod cannot
// package.
//
// For audio the direct branch hands the file to the browser and depends on it
// recognising the response. Go's builtin MIME table covers mp3, wav, ogg,
// flac, m4a and opus, but has no entry for m4b, aac, wma, ape, mka or wv, and
// the runtime image ships no mime.types to fill the gap — those arrive with a
// sniffed Content-Type and no guarantee the player takes them. Routing every
// added format through HLS keeps the outcome the same for all of them; AAC
// payloads remux rather than re-encode, so the cost is small.
//
// mp3/wav/ogg stay on the direct branch: they work today and moving them would
// spend CPU to fix nothing. flac/m4a predate this list's current reasoning.
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
