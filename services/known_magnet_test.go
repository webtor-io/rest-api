package services

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

const (
	trA2 = "udp://tracker.example:6969/announce?passkey=0123456789abcdef0123456789abcdef"
	trB  = "http://b.tracker.example/announce?passkey=0123456789abcdef0123456789abcdef"
)

// knownPrivate stubs a stored private hash: loader Touch internal ok, Pull → privateTorrent(t) stripped as stored.
func knownPrivate(t *testing.T, ts *TorrentStoreClientMock) {
	t.Helper()
	stored := resolvedPrivate(t)
	mockLoader(ts, mustInfoHash(t, stored), stored)
}

// spyVerify answers every Verify with reply and err and keeps the requests.
// Answered rather than left unexpected, so a call shows up as a call, not as
// a mock panic's 500.
func spyVerify(m2t *Magnet2TorrentClientMock, reply *m2tp.VerifyReply, err error) *[]*m2tp.VerifyRequest {
	got := &[]*m2tp.VerifyRequest{}
	m2t.On("Verify", mock.Anything, mock.Anything, mock.Anything).Run(func(a mock.Arguments) {
		*got = append(*got, a.Get(1).(*m2tp.VerifyRequest))
	}).Return(reply, err).Maybe()
	return got
}

// spyPush is spyVerify for torrent-store's Push.
func spyPush(ts *TorrentStoreClientMock, reply *tsp.PushReply, err error) *[]*tsp.PushRequest {
	got := &[]*tsp.PushRequest{}
	ts.On("Push", mock.Anything, mock.Anything, mock.Anything).Run(func(a mock.Arguments) {
		*got = append(*got, a.Get(1).(*tsp.PushRequest))
	}).Return(reply, err).Maybe()
	return got
}

// verifiedTier is the one tier of the one Verify call in verifies.
func verifiedTier(t *testing.T, verifies []*m2tp.VerifyRequest, ih string) []string {
	t.Helper()
	require.Len(t, verifies, 1)
	assert.Equal(t, ih, verifies[0].GetInfoHash())
	require.Len(t, verifies[0].GetTrackers(), 1)
	return verifies[0].GetTrackers()[0].GetUrls()
}

// assertMagnetPush checks the Push of a verified magnet: uid's, source
// magnet, never dry_run, the stored info dict with tier as its only tier
// and no web seeds.
func assertMagnetPush(t *testing.T, in *tsp.PushRequest, uid, ih string, tier []string) {
	t.Helper()
	assert.False(t, in.GetDryRun())
	assert.True(t, callerIs(uid, false)(in.GetCaller()), "caller %v", in.GetCaller())
	assert.Equal(t, "magnet", in.GetSource())
	mi, err := metainfo.Load(bytes.NewReader(in.GetTorrent()))
	require.NoError(t, err)
	assert.Equal(t, metainfo.AnnounceList{tier}, mi.AnnounceList)
	assert.Equal(t, tier[0], mi.Announce)
	assert.Empty(t, mi.UrlList)
	assert.Equal(t, ih, mi.HashInfoBytes().HexString())
}

// A magnet does not change an existing record.
func TestKnownPrivateMagnetAllowedSkipsVerify(t *testing.T) {
	h, _, ts, m2t := newPrivateWeb(t, true)
	knownPrivate(t, ts)
	ih := mustInfoHash(t, privateTorrent(t))
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).
		Return(&tsp.TouchReply{Access: "allowed"}, nil).Twice()
	verifies := spyVerify(m2t, &m2tp.VerifyReply{Outcome: "accepted"}, nil)
	pushes := spyPush(ts, &tsp.PushReply{RecordOutcome: "inserted"}, nil)

	for i := 0; i < 2; i++ { // nothing remembered: the store is asked each time
		rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), []byte(privateMagnet(t, trA2)))

		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), `"private":true`)
		assert.NotContains(t, rec.Body.String(), "verified")
		assert.NotContains(t, rec.Body.String(), "tracker.example")
	}
	assert.Empty(t, *verifies)
	assert.Empty(t, *pushes)
	ts.AssertExpectations(t) // the loader once, Touch of uidA twice
}

func TestKnownPrivateMagnetVerify(t *testing.T) {
	ih := mustInfoHash(t, privateTorrent(t))
	for _, tc := range []struct {
		name       string
		enforce    bool
		reply      *m2tp.VerifyReply
		err        error
		status     int
		code, why  string
		retryAfter string
		realPush   bool
		verified   bool
		refused    string // restapi_private_refused_total{post, refused} +1; "" none
	}{
		{"accepted", true, &m2tp.VerifyReply{Outcome: "accepted"}, nil, 200, "", "", "", true, true, ""},
		{"refused", true, &m2tp.VerifyReply{Outcome: "refused", Class: "key"}, nil, 422, "private_tracker_refused", "key", "", false, false, "private_tracker_refused"},
		{"invalid", true, &m2tp.VerifyReply{Outcome: "invalid"}, nil, 422, "private_no_tracker", "", "", false, false, "private_no_tracker"},
		{"unreachable", true, &m2tp.VerifyReply{Outcome: "unreachable"}, nil, 503, "private_verify_unavailable", "", "60", false, false, "private_verify_unavailable"},
		{"old m2t", true, nil, status.Error(codes.Unimplemented, "unknown method Verify"), 503, "private_verify_unavailable", "", "60", false, false, "private_verify_unavailable"},
		{"observe refused", false, &m2tp.VerifyReply{Outcome: "refused", Class: "key"}, nil, 200, "", "", "", false, false, "private_tracker_refused"},
		{"observe old m2t", false, nil, status.Error(codes.Unimplemented, "x"), 200, "", "", "", false, false, "private_verify_unavailable"},
		{"observe accepted", false, &m2tp.VerifyReply{Outcome: "accepted"}, nil, 200, "", "", "", true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, ts, m2t := newPrivateWeb(t, tc.enforce)
			knownPrivate(t, ts)
			ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).
				Return(touchRefused("no_record")()).Once()
			verifies := spyVerify(m2t, tc.reply, tc.err)
			pushes := spyPush(ts, &tsp.PushReply{RecordOutcome: "inserted"}, nil)
			uploads := privateUploadsTotal.WithLabelValues(sourceMagnet, "inserted")
			beforeUploads := counter(t, uploads)
			var beforeRefused float64
			if tc.refused != "" {
				beforeRefused = counter(t, privateRefusedTotal.WithLabelValues(routePost, tc.refused))
			}

			rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), []byte(privateMagnet(t, trA, trA2)))

			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, tc.retryAfter, rec.Header().Get("Retry-After"))
			if tc.status != 200 {
				e := errorBody(t, rec)
				assert.Equal(t, tc.code, e.Code)
				assert.Equal(t, tc.why, e.Reason)
			}
			assert.Equal(t, tc.verified, strings.Contains(rec.Body.String(), `"verified":"accepted"`), rec.Body.String())
			assert.Equal(t, []string{trA, trA2}, verifiedTier(t, *verifies, ih))
			if tc.realPush {
				require.Len(t, *pushes, 1)
				assertMagnetPush(t, (*pushes)[0], uidA, ih, []string{trA, trA2})
				assert.Equal(t, 1.0, counter(t, uploads)-beforeUploads)
			} else {
				assert.Empty(t, *pushes)
			}
			if tc.refused != "" {
				assert.Equal(t, 1.0, counter(t, privateRefusedTotal.WithLabelValues(routePost, tc.refused))-beforeRefused)
			}
		})
	}
}

// Controller addendum: a magnet with tr= for a stored private hash gets a
// decision before anything of the resource is answered.
func TestKnownPrivateMagnetRefusalHidesResource(t *testing.T) {
	h, _, ts, m2t := newPrivateWeb(t, true)
	knownPrivate(t, ts)
	ih := mustInfoHash(t, privateTorrent(t))
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).Return(touchRefused("no_record")())
	spyVerify(m2t, &m2tp.VerifyReply{Outcome: "refused", Class: "key"}, nil)

	rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), []byte(privateMagnet(t, trA)))

	require.Equal(t, 422, rec.Code, rec.Body.String())
	assert.Equal(t, codePrivateTrackerRefused, errorBody(t, rec).Code)
	for _, s := range []string{"probe", `"files_count"`, `"file"`, `"private"`} {
		assert.NotContains(t, rec.Body.String(), s)
	}
}

func TestKnownPrivateMagnetStoreAnswers(t *testing.T) {
	ih := mustInfoHash(t, privateTorrent(t))
	for _, mode := range accessModes {
		for _, tc := range []struct {
			name        string
			uid         string
			touch       func() (*tsp.TouchReply, error)
			push        error // Push's error; nil: inserted
			enforceCode int
			code, why   string
			refused     string
			calls       int // Verify and Push calls each
		}{
			{"sign in", "", touchRefused("sign_in"), nil, 403, "private_torrent", "sign_in", "sign_in", 0},
			{"disabled", uidA, func() (*tsp.TouchReply, error) {
				return nil, infoErr(codes.FailedPrecondition, "torrent-store", "private_unavailable")
			}, nil, 403, "private_unavailable", "", "private_unavailable", 0},
			{"push refused", uidA, touchRefused("no_record"), infoErr(codes.InvalidArgument, "torrent-store", "private_too_many_trackers"),
				422, "private_too_many_trackers", "", "private_too_many_trackers", 1},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				h, _, ts, m2t := newPrivateWeb(t, mode.enforce)
				knownPrivate(t, ts)
				ts.On("Touch", mock.Anything, touchOf(ih, callerIs(tc.uid, false)), mock.Anything).Return(tc.touch()).Once()
				verifies := spyVerify(m2t, &m2tp.VerifyReply{Outcome: "accepted"}, nil)
				var pushes *[]*tsp.PushRequest
				if tc.push != nil {
					pushes = spyPush(ts, nil, tc.push)
				} else {
					pushes = spyPush(ts, &tsp.PushReply{RecordOutcome: "inserted"}, nil)
				}
				refused := privateRefusedTotal.WithLabelValues(routePost, tc.refused)
				before := counter(t, refused)
				claims := jwt.MapClaims{}
				if tc.uid != "" {
					claims["uid"] = tc.uid
				}

				rec := send(t, h, "POST", "/resource/", signed(t, claims), []byte(privateMagnet(t, trA)))

				want := 200
				if mode.enforce {
					want = tc.enforceCode
				}
				require.Equal(t, want, rec.Code, rec.Body.String())
				if want != 200 {
					e := errorBody(t, rec)
					assert.Equal(t, tc.code, e.Code)
					assert.Equal(t, tc.why, e.Reason)
				} else {
					assert.NotContains(t, rec.Body.String(), "verified")
				}
				assert.Equal(t, 1.0, counter(t, refused)-before)
				assert.Len(t, *verifies, tc.calls)
				assert.Len(t, *pushes, tc.calls)
				ts.AssertExpectations(t)
			})
		}
	}
}

// A store that predates records answers Touch without access.
// Nothing is checked or pushed: an old store would merge the magnet's key
// into the shared copy.
func TestKnownMagnetOldStore(t *testing.T) {
	ih := mustInfoHash(t, privateTorrent(t))
	for _, mode := range accessModes {
		t.Run(mode.name, func(t *testing.T) {
			h, _, ts, m2t := newPrivateWeb(t, mode.enforce)
			knownPrivate(t, ts)
			ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidA, false)), mock.Anything).Return(&tsp.TouchReply{}, nil).Once()
			verifies := spyVerify(m2t, &m2tp.VerifyReply{Outcome: "accepted"}, nil)
			pushes := spyPush(ts, &tsp.PushReply{RecordOutcome: "inserted"}, nil)

			rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), []byte(privateMagnet(t, trA)))

			require.Equal(t, 503, rec.Code, rec.Body.String())
			assert.Equal(t, codeStoreOutdated, errorBody(t, rec).Code)
			assert.Empty(t, *verifies)
			assert.Empty(t, *pushes)
		})
	}
}

func TestKnownPrivateMagnetRateLimit(t *testing.T) {
	ih := mustInfoHash(t, privateTorrent(t))
	for _, mode := range accessModes {
		t.Run(mode.name, func(t *testing.T) {
			h, w, ts, m2t := newPrivateWeb(t, mode.enforce)
			knownPrivate(t, ts)
			ts.On("Touch", mock.Anything, touchOf(ih, func(c *tsp.Caller) bool { return c != nil && !c.GetInternal() }), mock.Anything).
				Return(touchRefused("no_record")())
			verifies := spyVerify(m2t, &m2tp.VerifyReply{Outcome: "refused", Class: "key"}, nil)
			for i := 0; i < 20; i++ { // the hour's budget of uidA is spent
				require.True(t, w.vl.allow(uidA, time.Now()), i)
			}
			limited := privateRefusedTotal.WithLabelValues(routePost, reasonRateLimited)
			before := counter(t, limited)

			rec := send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidA}), []byte(privateMagnet(t, trA)))

			if mode.enforce {
				require.Equal(t, 429, rec.Code, rec.Body.String())
				assert.Equal(t, "60", rec.Header().Get("Retry-After"))
				assert.Empty(t, errorBody(t, rec).Code)
			} else {
				require.Equal(t, 200, rec.Code, rec.Body.String())
			}
			assert.Equal(t, 1.0, counter(t, limited)-before)
			assert.Empty(t, *verifies)

			// the limit is per uid
			send(t, h, "POST", "/resource/", signed(t, jwt.MapClaims{"uid": uidB}), []byte(privateMagnet(t, trA)))
			assert.Len(t, *verifies, 1)
		})
	}
}

// Spec: two users, magnets of one unknown private hash at once — only the loader's owner skips Verify.
func TestTwoUsersUnknownPrivateMagnetSecondVerifies(t *testing.T) {
	h, _, ts, m2t := newPrivateWeb(t, true)
	ih := mustInfoHash(t, privateTorrent(t))
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs("", true)), mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	ts.On("Touch", mock.Anything, touchOf(ih, callerIs(uidB, false)), mock.Anything).Return(touchRefused("no_record")()).Once()
	started, release := blockingM2T(m2t, torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}}))
	verifies := spyVerify(m2t, &m2tp.VerifyReply{Outcome: "accepted"}, nil)
	pushes := spyPush(ts, &tsp.PushReply{RecordOutcome: "inserted"}, nil)
	post := func(uid, tr string) chan *httptest.ResponseRecorder {
		tok, body := signed(t, jwt.MapClaims{"uid": uid}), []byte(privateMagnet(t, tr))
		ch := make(chan *httptest.ResponseRecorder, 1)
		go func() { ch <- send(t, h, "POST", "/resource/", tok, body) }()
		return ch
	}

	first := post(uidA, trA)
	<-started // uidA's loader resolves the hash
	second := post(uidB, trB)
	time.Sleep(100 * time.Millisecond) // as in TestTwoUsersUnknownPrivateMagnetOneResolves
	close(release)

	a, b := <-first, <-second
	require.Equal(t, 200, a.Code, a.Body.String())
	assert.NotContains(t, a.Body.String(), "verified")
	require.Equal(t, 200, b.Code, b.Body.String())
	assert.Contains(t, b.Body.String(), `"verified":"accepted"`)
	assert.Equal(t, []string{trB}, verifiedTier(t, *verifies, ih))
	require.Len(t, *pushes, 2)
	assert.True(t, callerIs(uidA, false)((*pushes)[0].GetCaller()), "the loader pushes for its creator")
	assertMagnetPush(t, (*pushes)[1], uidB, ih, []string{trB})
	ts.AssertExpectations(t)
}
