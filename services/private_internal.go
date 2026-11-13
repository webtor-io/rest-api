package services

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	tsp "github.com/webtor-io/torrent-store/proto"
)

// The internal endpoints are thin wrappers over the torrent-store record
// RPCs for account export and deletion: one RPC per request, no query
// parameters, no others (S22). They are not part of the public API.

// PrivateTrackerRecord is one private record of a user.
type PrivateTrackerRecord struct {
	ResourceID string    `json:"resource_id"`
	Hosts      []string  `json:"hosts"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type DeletedResponse struct {
	Deleted int `json:"deleted"`
}

var errInternalOnly = &apiError{Status: http.StatusForbidden, Msg: "internal only"}

// internalStore checks that the caller is internal and :uid is a uid, and
// returns the uid with the store client.
func (s *Web) internalStore(g *gin.Context) (string, tsp.TorrentStoreClient, error) {
	if !readClaims(g, s.secret).Internal {
		return "", nil, errInternalOnly
	}
	uid := g.Param("uid")
	if !uidRe.MatchString(uid) {
		return "", nil, errors.New("failed to parse uid")
	}
	ts, err := s.rm.ts.Get()
	if err != nil {
		return "", nil, errStoreUnavailable
	}
	return uid, ts, nil
}

// internalStoreErr: a typed refusal as is (private_unavailable is the
// store's configuration, read as "no records"), any other failure 503 and
// never an empty answer.
func internalStoreErr(err error) error {
	var ae *apiError
	if errors.As(fromStore(err, false), &ae) {
		return ae
	}
	return errStoreUnavailable
}

// GET /internal/private-trackers/:uid: the user's private records.
func (s *Web) listPrivateTrackers(g *gin.Context) {
	uid, ts, err := s.internalStore(g)
	if err != nil {
		g.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(g.Request.Context(), s.rm.torrentStoreTimeout)
	defer cancel()
	rep, err := ts.ListPrivateRecords(ctx, &tsp.ListPrivateRecordsRequest{Uid: uid})
	if err != nil {
		g.Error(internalStoreErr(err))
		return
	}
	res := make([]PrivateTrackerRecord, 0, len(rep.GetRecords()))
	for _, r := range rep.GetRecords() {
		res = append(res, PrivateTrackerRecord{
			ResourceID: r.GetInfoHash(),
			Hosts:      r.GetHosts(),
			Source:     r.GetSource(),
			CreatedAt:  time.Unix(r.GetCreatedAt(), 0).UTC(),
			UpdatedAt:  time.Unix(r.GetUpdatedAt(), 0).UTC(),
		})
	}
	g.PureJSON(http.StatusOK, res)
}

// DELETE /internal/private-trackers/:uid: deletes all the user's private records.
func (s *Web) deletePrivateTrackers(g *gin.Context) {
	uid, ts, err := s.internalStore(g)
	if err != nil {
		g.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(g.Request.Context(), s.rm.torrentStoreTimeout)
	defer cancel()
	rep, err := ts.DeletePrivateRecordsByUser(ctx, &tsp.DeletePrivateRecordsByUserRequest{Uid: uid})
	if err != nil {
		g.Error(internalStoreErr(err))
		return
	}
	g.PureJSON(http.StatusOK, &DeletedResponse{Deleted: int(rep.GetDeleted())})
}
