package services

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	tsp "github.com/webtor-io/torrent-store/proto"
)

// zeroPieceLengthTorrent is a one-file torrent with "piece length" 0:
// it decodes, then parseTorrent divides by the piece length and panics.
var zeroPieceLengthTorrent = []byte("d4:infod6:lengthi1e4:name1:a12:piece lengthi0e6:pieces20:aaaaaaaaaaaaaaaaaaaaee")

// A panic inside a ResourceMap loader must come back as an error and leave
// the key loadable. On lazymap 24fcf0a the panic escaped Get and the next
// Get re-ran the first caller's f (with its context); from a258d93 until
// 55fd9cb every later Get of the key blocked forever.
func TestResourceMap_loaderPanicIsAnErrorAndKeyRetries(t *testing.T) {
	rm := NewTestResourceMap()
	tscl, _ := rm.ts.Get()
	ts := tscl.(*TorrentStoreClientMock)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, nil)
	ts.On("Pull", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PullReply{Torrent: zeroPieceLengthTorrent}, nil).Once()
	ts.On("Pull", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PullReply{Torrent: loadSintel(t)}, nil)

	type result struct {
		r        *Resource
		err      error
		panicked any
	}
	get := func() result {
		ch := make(chan result, 1)
		go func() {
			var res result
			defer func() {
				res.panicked = recover()
				ch <- res
			}()
			res.r, res.err = rm.Get(context.Background(), []byte("08ada5a7a6183aae1e09d831df6748d566095a10"))
		}()
		select {
		case res := <-ch:
			return res
		case <-time.After(5 * time.Second):
			t.Fatal("Get hung: the key is wedged after a panic in its loader")
			return result{}
		}
	}

	first := get()
	assert.Nil(t, first.panicked, "the loader's panic escaped Get")
	assert.Nil(t, first.r)
	assert.ErrorContains(t, first.err, "integer divide by zero")

	second := get()
	require.Nil(t, second.panicked, "the loader's panic escaped Get")
	require.NoError(t, second.err)
	assertSintel(t, second.r)
}

// A loader panic reaches errorHandler as an error, not through recovery. It
// is answered 500 whatever its text says ("not found" would be a 404 by the
// keyword match), with no panic value or stack in the body, and is logged
// and counted as a panic like the ones recovery catches.
func TestErrorHandler_loaderPanicIs500AndLogged(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()
	rm := NewTestResourceMap()
	tscl, _ := rm.ts.Get()
	ts := tscl.(*TorrentStoreClientMock)
	const value = "torrent not found: secret-value"
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Panic(value)

	w := &Web{rm: rm}
	r := newRouter()
	r.Use(w.errorHandler)
	r.POST("/resource/", w.postResource)
	panics := panicsTotal.WithLabelValues("/resource/")
	before := counter(t, panics)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/resource/", bytes.NewReader([]byte("08ada5a7a6183aae1e09d831df6748d566095a10"))))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":"Internal Server Error"}`, rec.Body.String())
	assert.Equal(t, 1.0, counter(t, panics)-before, "panics_total{/resource/} delta")
	var logged bool
	for _, e := range hook.AllEntries() {
		if e.Message == "panic recovered" {
			logged = true
			assert.Equal(t, value, e.Data["panic"])
			assert.Contains(t, e.Data["stack"], "goroutine ")
			assert.Equal(t, "/resource/", e.Data["path"])
		}
	}
	assert.True(t, logged, "no 'panic recovered' log entry")
}
