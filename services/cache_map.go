package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/urfave/cli"
	"github.com/webtor-io/lazymap"
)

const (
	useInternalTorrentHTTPProxyFlag = "use-internal-torrent-http-proxy"
	torrentHTTPProxyHostFlag        = "torrent-http-proxy-host"
	torrentHTTPProxyPortFlag        = "torrent-http-proxy-port"
)

func RegisterFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.BoolFlag{
			Name:   useInternalTorrentHTTPProxyFlag,
			Usage:  "use internal torrent http proxy",
			EnvVar: "USE_INTERNAL_TORRENT_HTTP_PROXY",
		},
		cli.StringFlag{
			Name:   torrentHTTPProxyHostFlag,
			Usage:  "torrent http proxy host",
			EnvVar: "TORRENT_HTTP_PROXY_SERVICE_HOST",
		},
		cli.IntFlag{
			Name:   torrentHTTPProxyPortFlag,
			Usage:  "torrent http proxy port",
			EnvVar: "TORRENT_HTTP_PROXY_SERVICE_PORT",
			Value:  80,
		},
	)
}

type CacheMap struct {
	lazymap.LazyMap[bool]
	cl                          *http.Client
	useInternalTorrentHTTPProxy bool
	torrentHTTPProxyHost        string
	torrentHTTPProxyPort        int
}

func NewCacheMap(c *cli.Context, cl *http.Client) *CacheMap {
	return &CacheMap{
		LazyMap: lazymap.New[bool](&lazymap.Config{
			Expire: 30 * time.Second,
		}),
		cl:                          cl,
		useInternalTorrentHTTPProxy: c.Bool(useInternalTorrentHTTPProxyFlag),
		torrentHTTPProxyHost:        c.String(torrentHTTPProxyHostFlag),
		torrentHTTPProxyPort:        c.Int(torrentHTTPProxyPortFlag),
	}
}

func (s *CacheMap) Get(u *MyURL) (bool, error) {
	return s.LazyMap.Get(u.Path, func() (bool, error) {
		cacheCtx, cacheCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cacheCancel()
		i, err := url.Parse(u.String())
		if err != nil {
			return false, err
		}
		if s.useInternalTorrentHTTPProxy {
			internal := fmt.Sprintf("%v:%v", s.torrentHTTPProxyHost, s.torrentHTTPProxyPort)
			i.Host = internal
			i.Scheme = "http"
		}
		q := u.Query()
		q.Set("done", "true")
		i.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(cacheCtx, "GET", i.String(), nil)
		if err != nil {
			return false, err
		}
		res, err := s.cl.Do(req)
		if err != nil {
			return false, err
		}
		defer func(Body io.ReadCloser) {
			_ = Body.Close()
		}(res.Body)
		return res.StatusCode == http.StatusOK, nil
	})
}
