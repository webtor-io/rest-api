package services

type MediaFormat string

const (
	Audio    MediaFormat = "audio"
	Video    MediaFormat = "video"
	Image    MediaFormat = "image"
	Subtitle MediaFormat = "subtitle"
	Unknown  MediaFormat = "unknown"
)

var formats = map[MediaFormat][]string{
	Video:    {"avi", "mkv", "mp4", "webm", "m4v", "ts", "vob"},
	Audio:    {"mp3", "wav", "ogg", "flac", "m4a"},
	Image:    {"png", "gif", "jpg", "jpeg"},
	Subtitle: {"srt", "vtt"},
}

var transcodeExt = []string{"avi", "mkv", "m4v", "ts", "vob", "flac", "m4a"}

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
