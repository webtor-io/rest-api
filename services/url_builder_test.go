package services

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type stubParamGetter struct{}

func (stubParamGetter) Param(string) string        { return "" }
func (stubParamGetter) Query(string) string        { return "" }
func (stubParamGetter) QueryArray(string) []string { return nil }
func (stubParamGetter) GetHeader(string) string    { return "" }

func newSubtitleStreamURLBuilder(ext string) *StreamURLBuilder {
	name := "ep01." + ext
	return &StreamURLBuilder{
		BaseURLBuilder: BaseURLBuilder{
			r: &Resource{ID: "abc", Name: "Show"},
			i: &ListItem{
				Name:        name,
				PathStr:     "/Show/" + name,
				Path:        []string{"Show", name},
				Type:        ListTypeFile,
				Ext:         ext,
				MediaFormat: getMediaFormatByExt(ext),
			},
			g: stubParamGetter{},
		},
	}
}

// srt2vtt sniffs SSA/ASS by content, not by extension, so every text subtitle
// format routes through the same ~vtt transform. The converted name must carry
// a .vtt extension whatever the source extension was — building it by trimming
// the literal "srt" produced "ep01.assvtt" for ASS.
func TestBuildSubtitleStreamURLConvertsTextFormats(t *testing.T) {
	for _, ext := range []string{"srt", "ass", "ssa"} {
		b := newSubtitleStreamURLBuilder(ext)
		u, err := b.BuildSubtitleStreamURL(&MyURL{})
		if err != nil {
			t.Fatalf("%v: unexpected error %v", ext, err)
		}
		if u == nil {
			t.Fatalf("%v: expected a stream url, got nil", ext)
		}
		want := ServiceSeparator + "vtt/ep01.vtt"
		if u.Path != want {
			t.Errorf("%v: path = %q, want %q", ext, u.Path, want)
		}
	}
}

// Negative control: formats srt2vtt cannot convert must yield no stream URL.
// vtt needs no conversion at all, and sub/idx/sup are binary.
func TestBuildSubtitleStreamURLSkipsNonConvertible(t *testing.T) {
	for _, ext := range []string{"sub", "idx", "sup"} {
		b := newSubtitleStreamURLBuilder(ext)
		u, err := b.BuildSubtitleStreamURL(&MyURL{})
		if err != nil {
			t.Fatalf("%v: unexpected error %v", ext, err)
		}
		if u != nil {
			t.Errorf("%v: expected no stream url, got %v", ext, u.Path)
		}
	}
}

// A media format with no stream URL of its own must export as "no stream
// item", not blow up on a nil URL: reaching url.String() through a nil *MyURL
// panics the request. Bitmap subtitles are the standing case — srt2vtt cannot
// convert them and they are served by no other route.
func TestStreamExporterHandlesAbsentURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ub := &URLBuilder{
		cm:     newTestCacheMap(srv.Client(), time.Second),
		domain: srv.URL,
	}
	e := NewStreamExporter(ub, NewTagBuilder(ub, nil))

	r := &Resource{ID: "abc", Name: "Show"}
	i := &ListItem{
		Name:        "ep01.sub",
		PathStr:     "/Show/ep01.sub",
		Path:        []string{"Show", "ep01.sub"},
		Type:        ListTypeFile,
		Ext:         "sub",
		MediaFormat: Subtitle,
	}

	ex, err := e.Export(r, i, stubParamGetter{})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if ex != nil {
		t.Errorf("expected no export item, got %+v", ex)
	}
}

// .vtt needs no conversion — it is already WebVTT — but it still has to yield
// a URL. Returning nil for it panicked two separate call sites that assumed a
// recognised subtitle always has one (see the tests below).
func TestBuildSubtitleStreamURLServesVTTDirectly(t *testing.T) {
	b := newSubtitleStreamURLBuilder("vtt")
	u, err := b.BuildSubtitleStreamURL(&MyURL{URL: url.URL{Path: "/abc/Show/ep01.vtt"}})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if u == nil {
		t.Fatal("expected a url for .vtt, got nil")
	}
	if strings.Contains(u.Path, ServiceSeparator+"vtt") {
		t.Errorf(".vtt must be served as-is, not routed through srt2vtt: %q", u.Path)
	}
	if u.Path != "/abc/Show/ep01.vtt" {
		t.Errorf("path = %q, want the file itself", u.Path)
	}
}

// A .vtt sidecar sitting next to a video is reached from the plain UI: opening
// the video builds its tag, which attaches same-named subtitle files as tracks.
// This is how a nil URL reached production as a 500 on every such torrent —
// courses ship a .vtt per lesson.
func TestBuildTrackHandlesVTTSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ub := &URLBuilder{cm: newTestCacheMap(srv.Client(), time.Second), domain: srv.URL}
	r := &Resource{ID: "abc", Name: "Show"}
	video := &ListItem{Name: "ep01.mp4", PathStr: "/Show/ep01.mp4", Path: []string{"Show", "ep01.mp4"},
		Type: ListTypeFile, Ext: "mp4", MediaFormat: Video}
	sub := ListItem{Name: "ep01.vtt", PathStr: "/Show/ep01.vtt", Path: []string{"Show", "ep01.vtt"},
		Type: ListTypeFile, Ext: "vtt", MediaFormat: Subtitle}

	tb := VideoTagBuider{BaseTagBuilder: BaseTagBuilder{ub: ub, r: r, i: video, g: stubParamGetter{}}}
	track, err := tb.BuildTrack(&sub)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if track == nil || track.Src == "" {
		t.Fatal("expected a subtitle track for the .vtt sidecar")
	}
}

// Negative control: a subtitle format with no servable URL must be skipped,
// not crashed on.
func TestBuildTrackSkipsUnservableSubtitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ub := &URLBuilder{cm: newTestCacheMap(srv.Client(), time.Second), domain: srv.URL}
	r := &Resource{ID: "abc", Name: "Show"}
	video := &ListItem{Name: "ep01.mp4", PathStr: "/Show/ep01.mp4", Path: []string{"Show", "ep01.mp4"},
		Type: ListTypeFile, Ext: "mp4", MediaFormat: Video}
	sub := ListItem{Name: "ep01.sub", PathStr: "/Show/ep01.sub", Path: []string{"Show", "ep01.sub"},
		Type: ListTypeFile, Ext: "sub", MediaFormat: Subtitle}

	tb := VideoTagBuider{BaseTagBuilder: BaseTagBuilder{ub: ub, r: r, i: video, g: stubParamGetter{}}}
	track, err := tb.BuildTrack(&sub)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if track != nil {
		t.Errorf("expected no track, got %+v", *track)
	}
}
