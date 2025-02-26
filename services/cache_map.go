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
	lazymap.LazyMap[bool]
	cl *http.Client
}

func NewCacheMap(cl *http.Client) *CacheMap {
	return &CacheMap{
		LazyMap: lazymap.New[bool](&lazymap.Config{
			Expire: 30 * time.Second,
		}),
		cl: cl,
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
