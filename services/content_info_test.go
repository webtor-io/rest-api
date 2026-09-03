package services

import "testing"

func TestGetMediaFormatByExt(t *testing.T) {
	for _, tt := range []struct {
		ext  string
		want MediaFormat
	}{
		// Video.
		{"avi", Video}, {"mkv", Video}, {"mp4", Video}, {"webm", Video},
		{"m4v", Video}, {"ts", Video}, {"vob", Video},
		{"mov", Video}, {"m2ts", Video},

		// Audio.
		{"mp3", Audio}, {"wav", Audio}, {"ogg", Audio}, {"flac", Audio},
		{"m4a", Audio}, {"m4b", Audio}, {"aac", Audio}, {"opus", Audio},
		{"wma", Audio}, {"ape", Audio}, {"mka", Audio}, {"wv", Audio},

		// Image.
		{"png", Image}, {"gif", Image}, {"jpg", Image}, {"jpeg", Image},
		{"webp", Image}, {"bmp", Image}, {"avif", Image},

		// Subtitle.
		{"srt", Subtitle}, {"vtt", Subtitle}, {"ass", Subtitle}, {"ssa", Subtitle},

		// Negative controls. Every one of these has been seen in real
		// torrents (sample of 6000 from torrent-store, 2026-09-04) and must
		// stay Unknown: showing a play button we cannot honour is worse than
		// offering an honest download.
		//
		// Archives and executables carry no playable stream at all.
		{"zip", Unknown}, {"rar", Unknown}, {"7z", Unknown}, {"iso", Unknown},
		{"exe", Unknown}, {"bin", Unknown},
		// Documents: the browser may render some of them, but they are not
		// media and must not reach the stream exporter.
		{"epub", Unknown}, {"pdf", Unknown}, {"fb2", Unknown}, {"cbz", Unknown},
		// tif decodes nowhere in a browser — a play button would open garbage.
		{"tif", Unknown}, {"tiff", Unknown},
		// Binary subtitle formats: srt2vtt only understands text cues.
		{"sub", Unknown}, {"idx", Unknown}, {"sup", Unknown},
		// Video containers deliberately left out: they need a full video
		// re-encode (VC-1 / MPEG-2 / RV40), not a remux.
		{"wmv", Unknown}, {"mpg", Unknown}, {"mpeg", Unknown},
		{"flv", Unknown}, {"rmvb", Unknown}, {"3gp", Unknown},
		// Not an extension at all.
		{"", Unknown},
	} {
		if got := getMediaFormatByExt(tt.ext); got != tt.want {
			t.Errorf("getMediaFormatByExt(%q) = %v, want %v", tt.ext, got, tt.want)
		}
	}
}

func TestShouldTranscode(t *testing.T) {
	for _, tt := range []struct {
		ext  string
		want bool
	}{
		// Video containers nginx-vod cannot package.
		{"avi", true}, {"mkv", true}, {"m4v", true}, {"ts", true},
		{"vob", true}, {"mov", true}, {"m2ts", true},

		// Audio the browser cannot be trusted to play from a bare file
		// response: the alpine image ships no mime.types, so
		// mime.TypeByExtension returns "" for every audio extension and the
		// player gets no Content-Type to work with. HLS makes it explicit.
		{"flac", true}, {"m4a", true}, {"m4b", true}, {"aac", true},
		{"opus", true}, {"wma", true}, {"ape", true}, {"mka", true}, {"wv", true},

		// Negative controls. These are served as-is today and must keep
		// bypassing the transcoder — mp4/webm go to nginx-vod, and the three
		// audio formats below are handed to the browser directly.
		{"mp4", false}, {"webm", false},
		{"mp3", false}, {"wav", false}, {"ogg", false},
		// Images and subtitles never route through the transcoder.
		{"jpg", false}, {"png", false}, {"webp", false}, {"bmp", false},
		{"avif", false}, {"srt", false}, {"vtt", false}, {"ass", false},
		{"ssa", false},
		// Unknown extensions.
		{"zip", false}, {"exe", false}, {"", false},
	} {
		if got := shouldTranscode(tt.ext); got != tt.want {
			t.Errorf("shouldTranscode(%q) = %v, want %v", tt.ext, got, tt.want)
		}
	}
}
