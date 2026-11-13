package services

import (
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tsp "github.com/webtor-io/torrent-store/proto"
)

var internalMethods = []string{"GET", "DELETE"}

// storeMethods are the store calls made so far, in order.
func storeMethods(ts *TorrentStoreClientMock) []string {
	var ms []string
	for _, c := range ts.Calls {
		ms = append(ms, c.Method)
	}
	return ms
}

// mockInternal answers the RPC behind method for uid once: err, or without
// it an empty list or {Deleted: 2}.
func mockInternal(ts *TorrentStoreClientMock, method, uid string, err error) {
	switch method {
	case "GET":
		var rep *tsp.ListPrivateRecordsReply
		if err == nil {
			rep = &tsp.ListPrivateRecordsReply{}
		}
		ts.On("ListPrivateRecords", mock.Anything, mock.MatchedBy(func(in *tsp.ListPrivateRecordsRequest) bool {
			return in.GetUid() == uid
		}), mock.Anything).Return(rep, err).Once()
	case "DELETE":
		var rep *tsp.DeletePrivateRecordsReply
		if err == nil {
			rep = &tsp.DeletePrivateRecordsReply{Deleted: 2}
		}
		ts.On("DeletePrivateRecordsByUser", mock.Anything, mock.MatchedBy(func(in *tsp.DeletePrivateRecordsByUserRequest) bool {
			return in.GetUid() == uid
		}), mock.Anything).Return(rep, err).Once()
	}
}

func TestInternalEndpointsNeedInternal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		claims jwt.MapClaims // nil: no X-Token
	}{
		{"no token", nil},
		{"user", jwt.MapClaims{"uid": uidA}},
		{"internal false", jwt.MapClaims{"internal": false}},
	} {
		for _, method := range internalMethods {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				h, _, ts, _ := newPrivateWeb(t, true)
				tok := ""
				if tc.claims != nil {
					tok = signed(t, tc.claims)
				}
				rec := send(t, h, method, "/internal/private-trackers/"+uidA, tok, nil)
				require.Equal(t, 403, rec.Code, rec.Body.String())
				assert.Equal(t, ErrorResponse{Error: "internal only"}, errorBody(t, rec))
				assert.Empty(t, storeMethods(ts))
			})
		}
	}
}

func TestInternalEndpointsOneRPC(t *testing.T) {
	h, _, ts, _ := newPrivateWeb(t, true)
	internal := signed(t, jwt.MapClaims{"internal": true})

	// ?host= is not read: both act on all the user's records (S7).
	ts.On("ListPrivateRecords", mock.Anything, mock.MatchedBy(func(in *tsp.ListPrivateRecordsRequest) bool { return in.GetUid() == uidA }), mock.Anything).
		Return(&tsp.ListPrivateRecordsReply{Records: []*tsp.PrivateRecordInfo{{InfoHash: tokenTestHash,
			Hosts: []string{"tracker.example"}, Source: "site", CreatedAt: 1700000000, UpdatedAt: 1700000100}}}, nil).Once()
	rec := send(t, h, "GET", "/internal/private-trackers/"+uidA+"?host=tracker.example", internal, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `[{"resource_id":"08ada5a7a6183aae1e09d831df6748d566095a10","hosts":["tracker.example"],"source":"site",
		"created_at":"2023-11-14T22:13:20Z","updated_at":"2023-11-14T22:15:00Z"}]`, rec.Body.String())
	assert.Equal(t, []string{"ListPrivateRecords"}, storeMethods(ts))

	mockInternal(ts, "DELETE", uidA, nil)
	rec = send(t, h, "DELETE", "/internal/private-trackers/"+uidA+"?host=tracker.example", internal, nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"deleted":2}`, rec.Body.String())
	assert.Equal(t, []string{"ListPrivateRecords", "DeletePrivateRecordsByUser"}, storeMethods(ts))

	// No delete by hash (S22): there is no such route.
	rec = send(t, h, "DELETE", "/internal/private-trackers/by-hash/"+tokenTestHash, internal, nil)
	assert.Equal(t, 404, rec.Code, rec.Body.String())
	assert.Len(t, ts.Calls, 2)
	ts.AssertExpectations(t)
}

func TestInternalEndpointsErrors(t *testing.T) {
	internal := signed(t, jwt.MapClaims{"internal": true})
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		// the store has no S3 or no key: web-ui reads it as "no records"
		{"private unavailable", infoErr(codes.FailedPrecondition, "torrent-store", "private_unavailable"), 403, "private_unavailable"},
		// a failure is never an empty list
		{"store down", status.Error(codes.Unavailable, "s3"), 503, ""},
	} {
		for _, method := range internalMethods {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				h, _, ts, _ := newPrivateWeb(t, true)
				mockInternal(ts, method, uidA, tc.err)
				rec := send(t, h, method, "/internal/private-trackers/"+uidA, internal, nil)
				require.Equal(t, tc.status, rec.Code, rec.Body.String())
				assert.Equal(t, tc.code, errorBody(t, rec).Code)
				assert.Len(t, ts.Calls, 1)
				ts.AssertExpectations(t)
			})
		}
	}
	for _, method := range internalMethods {
		t.Run("bad uid/"+method, func(t *testing.T) {
			h, _, ts, _ := newPrivateWeb(t, true)
			mockInternal(ts, method, "XYZ", nil) // reached only without the uid check
			rec := send(t, h, method, "/internal/private-trackers/XYZ", internal, nil)
			require.Equal(t, 400, rec.Code, rec.Body.String())
			assert.Equal(t, "failed to parse uid", errorBody(t, rec).Error)
			assert.Empty(t, storeMethods(ts))
		})
	}
	t.Run("empty list", func(t *testing.T) {
		h, _, ts, _ := newPrivateWeb(t, true)
		mockInternal(ts, "GET", uidA, nil)
		rec := send(t, h, "GET", "/internal/private-trackers/"+uidA, internal, nil)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.JSONEq(t, "[]", rec.Body.String()) // not null
	})
}
