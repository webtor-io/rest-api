package services

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

const (
	// magnet2torrent's Verify takes up to ~20 s (15 s for started, 5 s for
	// stopped); a shorter deadline would turn an accepted check into a
	// DeadlineExceeded.
	verifyTimeout    = 25 * time.Second
	verifyRetryAfter = 60 // seconds, on the 503 of a failed check and on the 429
)

var (
	errVerifyLimited = &apiError{Status: http.StatusTooManyRequests,
		Msg: "too many tracker checks, try again later", RetryAfter: verifyRetryAfter}
	errVerifyUnavailable = &apiError{Status: http.StatusServiceUnavailable, Code: codePrivateVerifyUnavailable,
		Msg: "private torrent: the tracker check is unavailable, try again later", RetryAfter: verifyRetryAfter}
	errVerifyInvalid = &apiError{Status: http.StatusUnprocessableEntity, Code: codePrivateNoTracker,
		Msg: "private torrent: this magnet has no usable tracker"}
)

// knownPrivateMagnet decides a magnet with tr= for a stored private hash;
// verified is "accepted" or "". A record of the caller's lets it through as
// it is: a magnet never changes a record, a new key takes the .torrent.
// Without one, the magnet's own trackers are asked (Verify), and only an
// accepting tracker makes a record. Refusals go through refuse; a store that
// cannot answer, or predates records, is a 503 in both modes before anything
// is checked or pushed: an old store would merge the key into the shared copy.
func (s *Web) knownPrivateMagnet(g *gin.Context, r *Resource, b []byte, cl callerClaims) (verified string, err error) {
	ctx := g.Request.Context()
	err = s.rm.touch(ctx, r.ID, &tsp.Caller{Uid: cl.UID})
	var ae *apiError
	switch {
	case err == nil:
		return "", nil
	case !errors.As(err, &ae) || ae.Status != http.StatusForbidden:
		return "", err
	case ae.Reason != reasonNoRecord: // sign_in, private_unavailable
		return "", s.refuse(g, routePost, ae)
	}
	if !s.vl.allow(cl.UID, time.Now()) {
		return "", s.refuse(g, routePost, errVerifyLimited)
	}
	tier := magnetTier(b)
	m2t, err := s.rm.m2t.Get()
	var rep *m2tp.VerifyReply
	if err == nil {
		vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
		rep, err = m2t.Verify(vctx, &m2tp.VerifyRequest{InfoHash: r.ID, Trackers: []*m2tp.VerifyTier{{Urls: tier}}})
		cancel()
	}
	if err != nil { // Unimplemented from an older magnet2torrent too
		return "", s.refuse(g, routePost, errVerifyUnavailable)
	}
	switch rep.GetOutcome() {
	case "accepted":
	case "refused":
		return "", s.refuse(g, routePost, &apiError{Status: http.StatusUnprocessableEntity, Code: codePrivateTrackerRefused,
			Reason: rep.GetClass(), Msg: "private torrent: the tracker of this magnet refused it"})
	case "invalid":
		return "", s.refuse(g, routePost, errVerifyInvalid)
	default: // unreachable, or an outcome this version does not know
		return "", s.refuse(g, routePost, errVerifyUnavailable)
	}
	body, err := oneTierTorrent(r.Torrent, tier)
	if err != nil {
		return "", err
	}
	if _, err := s.rm.pushPrivate(ctx, body, Uploader{cl.UID, sourceMagnet}); err != nil {
		if errors.As(err, &ae) && ae.Status < http.StatusInternalServerError {
			return "", s.refuse(g, routePost, ae)
		}
		return "", err
	}
	return "accepted", nil
}

// oneTierTorrent is base's info dict with Announce tier[0], AnnounceList {tier}, no UrlList.
func oneTierTorrent(base []byte, tier []string) ([]byte, error) {
	mi, err := metainfo.Load(bytes.NewReader(base))
	if err != nil {
		return nil, err
	}
	mi.Announce, mi.AnnounceList, mi.UrlList = tier[0], metainfo.AnnounceList{tier}, nil
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
