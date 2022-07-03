package services

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/urfave/cli"
	"github.com/webtor-io/lazymap"
)

type CompletedPieces map[[20]byte]bool

func NewCompletedPiecesFromReader(r io.Reader) (CompletedPieces, error) {
	data, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	cp := CompletedPieces{}
	for _, p := range splitPieces(data) {
		cp[p] = true
	}
	return cp, nil
}

func (cp CompletedPieces) Has(h Hash) bool {
	_, ok := map[[20]byte]bool(cp)[h]
	return ok
}

func (cp CompletedPieces) HasAny(hs []Hash) bool {
	for _, h := range hs {
		if cp.Has(h) {
			return true
		}
	}
	return false
}

type CompletedPiecesMap struct {
	lazymap.LazyMap
	cl      *http.Client
	host    string
	port    int
	timeout time.Duration
}

const (
	torrentWebCacheHostFlag = "torrent-web-cache-host"
	torrentWebCachePortFlag = "torrent-web-cache-port"
)

func RegisterTorrentWebCacheFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   torrentWebCacheHostFlag,
			Usage:  "torrent web cache host",
			Value:  "",
			EnvVar: "TORRENT_WEB_CACHE_SERVICE_HOST, TORRENT_WEB_CACHE_HOST",
		},
		cli.IntFlag{
			Name:   torrentWebCachePortFlag,
			Usage:  "torrent web cache port",
			Value:  80,
			EnvVar: "TORRENT_WEB_CACHE_SERVICE_PORT, TORRENT_WEB_CACHE_SERVICE_PORT",
		},
	)
}

func NewCompletedPiecesMap(c *cli.Context, cl *http.Client) *CompletedPiecesMap {
	return &CompletedPiecesMap{
		LazyMap: lazymap.New(&lazymap.Config{
			Concurrency: 100,
			Expire:      60 * time.Second,
			ErrorExpire: 30 * time.Second,
			Capacity:    1000,
		}),
		cl:      cl,
		host:    c.String(torrentWebCacheHostFlag),
		port:    c.Int(torrentWebCachePortFlag),
		timeout: 30 * time.Second,
	}
}

func (s *CompletedPiecesMap) get(hash string) (CompletedPieces, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	url := fmt.Sprintf("http://%v:%v/completed_pieces?hash=%v", s.host, s.port, hash)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return CompletedPieces{}, nil
	}
	return NewCompletedPiecesFromReader(resp.Body)
}

func (s *CompletedPiecesMap) Get(hash string) (CompletedPieces, error) {
	if s.host == "" {
		return CompletedPieces{}, nil
	}
	res, err := s.LazyMap.Get(hash, func() (interface{}, error) {
		return s.get(hash)
	})
	if err != nil {
		return nil, err
	}
	return res.(CompletedPieces), nil
}
