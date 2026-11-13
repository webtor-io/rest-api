package services

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
