package services

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"github.com/webtor-io/lazymap"
)

const (
	useInternalTorrentHTTPProxyFlag = "use-internal-torrent-http-proxy"
	torrentHTTPProxyHostFlag        = "torrent-http-proxy-host"
	torrentHTTPProxyPortFlag        = "torrent-http-proxy-port"
	cacheProbeTimeoutFlag           = "cache-probe-timeout"
)

func RegisterCacheMapFlags(f []cli.Flag) []cli.Flag {
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
		cli.DurationFlag{
			Name:   cacheProbeTimeoutFlag,
			Usage:  "cache probe timeout",
			EnvVar: "CACHE_PROBE_TIMEOUT",
			Value:  5 * time.Second,
		},
	)
}

type CacheMap struct {
	*lazymap.LazyMap[bool]
	cl                          *http.Client
	useInternalTorrentHTTPProxy bool
	torrentHTTPProxyHost        string
	torrentHTTPProxyPort        int
	probeTimeout                time.Duration
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
		probeTimeout:                c.Duration(cacheProbeTimeoutFlag),
	}
}

// Get reports whether the content behind u is already fully downloaded by the
// seeder. The answer only fills the advisory meta.cache flag in the export
// response, so it must never fail the export itself: any probe error degrades
// to "not cached". See the comment at the s.cl.Do error branch.
func (s *CacheMap) Get(u *MyURL) (bool, error) {
	return s.LazyMap.Get(u.Path, func() (bool, error) {
		cacheCtx, cacheCancel := context.WithTimeout(context.Background(), s.probeTimeout)
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
			// A dead upstream (edge proxy down, seeder still cold-starting
			// past the deadline) must not turn a URL-minting endpoint into a
			// 500 — an uncached answer is always a safe fallback, and a
			// seeder that can't answer within the deadline is not "done" by
			// any useful definition anyway. Memoizing the negative for the
			// map's TTL also keeps a flapping upstream from being re-probed
			// on every request.
			log.WithError(err).Warnf("failed to probe cache state for %v", u.Path)
			return false, nil
		}
		defer func(Body io.ReadCloser) {
			_ = Body.Close()
		}(res.Body)
		return res.StatusCode == http.StatusOK, nil
	})
}
