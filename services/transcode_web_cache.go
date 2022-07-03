package services

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/urfave/cli"
	"github.com/webtor-io/lazymap"
)

type TranscodeDoneMap struct {
	lazymap.LazyMap
	cl      *http.Client
	host    string
	port    int
	timeout time.Duration
}

const (
	transcodeWebCacheHostFlag = "transcode-web-cache-host"
	transcodeWebCachePortFlag = "transcode-web-cache-port"
)

func RegisterTranscodeWebCacheFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   transcodeWebCacheHostFlag,
			Usage:  "transcode web cache host",
			Value:  "",
			EnvVar: "TRANSCODE_WEB_CACHE_SERVICE_HOST, TRANSCODE_WEB_CACHE_HOST",
		},
		cli.IntFlag{
			Name:   transcodeWebCachePortFlag,
			Usage:  "transcode web cache port",
			Value:  80,
			EnvVar: "TRANSCODE_WEB_CACHE_SERVICE_PORT, TRANSCODE_WEB_CACHE_SERVICE_PORT",
		},
	)
}

func NewTranscodeDoneMap(c *cli.Context, cl *http.Client) *TranscodeDoneMap {
	return &TranscodeDoneMap{
		LazyMap: lazymap.New(&lazymap.Config{
			Concurrency: 100,
			Expire:      60 * time.Second,
			ErrorExpire: 30 * time.Second,
			Capacity:    1000,
		}),
		cl:      cl,
		host:    c.String(transcodeWebCacheHostFlag),
		port:    c.Int(transcodeWebCachePortFlag),
		timeout: 30 * time.Second,
	}
}

func (s *TranscodeDoneMap) get(prefix string, hash string, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	url := fmt.Sprintf("http://%v:%v/done", s.host, s.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	q := req.URL.Query()
	q.Add("prefix", prefix)
	q.Add("hash", hash)
	q.Add("path", path)
	req.URL.RawQuery = q.Encode()
	if err != nil {
		return false, err
	}
	resp, err := s.cl.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return true, nil
}

func (s *TranscodeDoneMap) Get(prefix string, hash string, path string) (bool, error) {
	if s.host == "" {
		return false, nil
	}
	key := fmt.Sprintf("%v-%v-%v", prefix, hash, path)
	res, err := s.LazyMap.Get(key, func() (interface{}, error) {
		return s.get(prefix, hash, path)
	})
	if err != nil {
		return false, err
	}
	return res.(bool), nil
}
