package services

import (
	"net/http"
	"net/http/httptest"
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
	for _, ext := range []string{"vtt", "sub", "idx", "sup"} {
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

// A media format with no stream URL of its own (.vtt is already WebVTT, so
// nothing is built for it) must export as "no stream item", not blow up on a
// nil URL. Reaching url.String() through a nil *MyURL panics the request.
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
		Name:        "ep01.vtt",
		PathStr:     "/Show/ep01.vtt",
		Path:        []string{"Show", "ep01.vtt"},
		Type:        ListTypeFile,
		Ext:         "vtt",
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
