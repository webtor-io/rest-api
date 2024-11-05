package services

import (
	"context"
	"github.com/webtor-io/lazymap"
	"io"
	"net/http"
	"net/url"
	"time"
)

type CacheMap struct {
	lazymap.LazyMap
	cl *http.Client
}

func NewCacheMap(cl *http.Client) *CacheMap {
	return &CacheMap{
		LazyMap: lazymap.New(&lazymap.Config{
			Expire:      30 * time.Second,
			ErrorExpire: 5 * time.Second,
		}),
		cl: cl,
	}
}

func (s *CacheMap) Get(ctx context.Context, url *MyURL) (bool, error) {
	res, err := s.LazyMap.Get(url.String(), func() (interface{}, error) {
		return s.get(ctx, url)
	})
	if err != nil {
		return false, err
	}
	return res.(bool), nil
}

func (s *CacheMap) get(ctx context.Context, u *MyURL) (bool, error) {
	i, err := url.Parse(u.String())
	if err != nil {
		return false, err
	}
	q := u.Query()
	q.Set("done", "true")
	i.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", i.String(), nil)
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
}
