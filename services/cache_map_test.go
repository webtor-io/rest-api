package services

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/webtor-io/lazymap"
)

func newTestCacheMap(cl *http.Client, timeout time.Duration) *CacheMap {
	return &CacheMap{
		LazyMap: lazymap.New[bool](&lazymap.Config{
			Expire: 30 * time.Second,
		}),
		cl:           cl,
		probeTimeout: timeout,
	}
}

func testCacheMapURL(t *testing.T, base string, path string) *MyURL {
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("failed to parse test server url %v: %v", base, err)
	}
	return &MyURL{URL: url.URL{Scheme: u.Scheme, Host: u.Host, Path: path}}
}

// An unreachable probe target must degrade to "not cached" instead of failing
// the export it is called from — losing the whole /export response over an
// advisory flag is what turned an edge-proxy outage into blanket 500s.
func TestCacheMapGetSwallowsProbeErrors(t *testing.T) {
	assert := assert.New(t)

	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer stalled.Close()

	cm := newTestCacheMap(stalled.Client(), 50*time.Millisecond)
	cached, err := cm.Get(testCacheMapURL(t, stalled.URL, "/timed-out"))
	assert.Nil(err)
	assert.False(cached)

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadCl, deadURL := dead.Client(), dead.URL
	dead.Close()

	cm = newTestCacheMap(deadCl, time.Second)
	cached, err = cm.Get(testCacheMapURL(t, deadURL, "/refused"))
	assert.Nil(err)
	assert.False(cached)
}

// The probe still has to report the actual cache state when the upstream
// answers: 200 means the seeder holds the complete content, anything else
// (404 for a partially downloaded file) means it does not.
func TestCacheMapGetReportsUpstreamStatus(t *testing.T) {
	assert := assert.New(t)

	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		if r.URL.Path == "/done" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cm := newTestCacheMap(srv.Client(), 5*time.Second)

	cached, err := cm.Get(testCacheMapURL(t, srv.URL, "/done"))
	assert.Nil(err)
	assert.True(cached)
	assert.Equal("true", gotQuery.Get("done"))

	cached, err = cm.Get(testCacheMapURL(t, srv.URL, "/partial"))
	assert.Nil(err)
	assert.False(cached)
}
