package services

import (
	"net/http"

	"github.com/pkg/errors"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// apiError is a refusal the client must tell apart: errorHandler answers it
// with its own status, code and reason, ahead of the keyword match.
type apiError struct {
	Status     int
	Code       string // ErrorResponse.Code
	Reason     string // ErrorResponse.Reason
	Msg        string // ErrorResponse.Error: English, no URL, no key
	RetryAfter int    // seconds; 0 = no Retry-After header
}

func (e *apiError) Error() string { return e.Msg }

const (
	codePrivateTorrent           = "private_torrent"
	codePrivateTorrentFile       = "private_torrent_file"
	codePrivateNeedsAccount      = "private_needs_account"
	codePrivateNoTracker         = "private_no_tracker"
	codePrivateTooManyTrackers   = "private_too_many_trackers"
	codePrivateMagnet            = "private_magnet"
	codePrivateTrackerRefused    = "private_tracker_refused"
	codePrivateVerifyUnavailable = "private_verify_unavailable"
	codePrivateUnavailable       = "private_unavailable"
	codeStoreOutdated            = "store_outdated"

	reasonSignIn       = "sign_in"
	reasonNoRecord     = "no_record"
	reasonTokenInQuery = "token_in_query"

	textPrivateTorrent = "private torrent: only a signed-in user who added it with their own tracker can play it"
)

// errorInfoReason returns the Reason of the first errdetails.ErrorInfo in a gRPC status (any Domain).
func errorInfoReason(err error) (reason string, ok bool) {
	for _, d := range status.Convert(err).Details() {
		if info, isInfo := d.(*errdetails.ErrorInfo); isInfo {
			return info.GetReason(), true
		}
	}
	return "", false
}

// fromStore turns a torrent-store or magnet2torrent error into what the HTTP layer answers.
// upload is set for Push and magnet resolution: private_unavailable refuses
// an upload with 422 and a read with 403. The upload refusals keep the
// store's text, which is English with no URL and no key.
func fromStore(err error, upload bool) error {
	reason, _ := errorInfoReason(err)
	switch reason {
	case codePrivateNeedsAccount, codePrivateNoTracker, codePrivateTooManyTrackers:
		return &apiError{Status: http.StatusUnprocessableEntity, Code: reason, Msg: status.Convert(err).Message()}
	case codePrivateUnavailable:
		code := http.StatusForbidden
		if upload {
			code = http.StatusUnprocessableEntity
		}
		return &apiError{Status: code, Code: codePrivateUnavailable, Msg: status.Convert(err).Message()}
	case reasonSignIn, reasonNoRecord:
		return &apiError{Status: http.StatusForbidden, Code: codePrivateTorrent, Reason: reason, Msg: textPrivateTorrent}
	case codePrivateMagnet:
		return errPrivateMagnet
	}
	// No ErrorInfo, or a reason this version does not know: as before
	// private records. PermissionDenied is a ban or the stoplist, and the
	// "forbidden" keyword makes errorHandler answer it 403 instead of 500.
	if status.Code(err) == codes.PermissionDenied {
		return errors.Wrap(err, "forbidden")
	}
	return err
}
