package services

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	tsp "github.com/webtor-io/torrent-store/proto"
)

// route labels of restapi_private_refused_total
const (
	routeResource = "resource"
	routeList     = "list"
	routeExport   = "export"
	routeTorrent  = "torrent"
	routePost     = "post"
)

// reason labels that are not response reasons
const (
	reasonRateLimited = "rate_limited"
	reasonNoToken     = "no_token"
)

var (
	// The info dict is the pass to a record of one's own, so a private
	// torrent's .torrent goes to internal callers only, even to its uploader.
	errPrivateTorrentFile = &apiError{Status: http.StatusForbidden, Code: codePrivateTorrentFile,
		Msg: "private torrent: its .torrent file is not served"}
	// The caller is read from X-Token only, and export would build a
	// ?token= into its links in place of the checked one.
	errTokenInQuery = &apiError{Status: http.StatusForbidden, Code: codePrivateTorrent,
		Reason: reasonTokenInQuery, Msg: textPrivateTorrent}
)

// resignedParams hands every URL builder the re-signed token: GetHeader("X-Token") → token.
// Query is not overridden: a request with ?token= is refused before any re-sign (token_in_query).
type resignedParams struct {
	ParamGetter
	token string
}

func (p resignedParams) GetHeader(k string) string {
	if http.CanonicalHeaderKey(k) == "X-Token" {
		return p.token
	}
	return p.ParamGetter.GetHeader(k)
}

// refuse counts a refusal on route and applies the mode: ae in enforce, nil
// in observe, where the request goes on as it did before. The label is the
// code, for private_torrent its reason; a 429 has no code (rate_limited). A
// sign_in to a request without X-Token is no_token (Р11): THP's privacy
// lookup comes that way, once per node and hash, and would drown the
// refusals of people. The answer does not change.
func (s *Web) refuse(g *gin.Context, route string, ae *apiError) error {
	label := ae.Code
	switch {
	case ae.Status == http.StatusTooManyRequests:
		label = reasonRateLimited
	case ae.Reason == reasonSignIn && g.GetHeader("X-Token") == "":
		label = reasonNoToken
	case ae.Code == codePrivateTorrent:
		label = ae.Reason
	}
	privateRefusedTotal.WithLabelValues(route, label).Inc()
	if s.enforce {
		return ae
	}
	return nil
}

// privateAllowed applies the access rule to r for this request and returns
// the ParamGetter export must build its URLs with. A public r costs nothing.
// A refusal goes through refuse; a store that cannot answer is a 503 in both
// modes. Only an allowance re-signs export's token, in both modes: a refusal
// in observe keeps the request's token as it was.
func (s *Web) privateAllowed(g *gin.Context, r *Resource, route string) (ParamGetter, error) {
	if !r.Private {
		return g, nil
	}
	cl := readClaims(g, s.secret)
	if route == routeTorrent {
		if cl.Internal {
			return g, nil
		}
		return g, s.refuse(g, route, errPrivateTorrentFile)
	}
	if g.Query("token") != "" {
		return g, s.refuse(g, route, errTokenInQuery)
	}
	c := &tsp.Caller{Uid: cl.UID, Internal: cl.Internal}
	if route == routeExport {
		c.Internal = false // the seeder serves a link to its uid only
	}
	err := s.rm.touch(g.Request.Context(), r.ID, c)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
		return g, s.refuse(g, route, ae)
	}
	if err != nil {
		return nil, err
	}
	if route != routeExport {
		return g, nil
	}
	tok, err := cl.resign(s.secret, r.ID, time.Now())
	if err != nil {
		return nil, err
	}
	return resignedParams{g, tok}, nil
}
