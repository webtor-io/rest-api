package services

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

// torrentWithTiers is base's info dict with the given announce list, as the new magnet2torrent returns it.
func torrentWithTiers(t *testing.T, base []byte, tiers [][]string) []byte {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(base))
	require.NoError(t, err)
	mi.Announce, mi.AnnounceList, mi.UrlList = "", tiers, nil
	var buf bytes.Buffer
	require.NoError(t, mi.Write(&buf))
	return buf.Bytes()
}

const trA = "http://tracker.example/announce?passkey=0123456789abcdef0123456789abcdef"

func callerIs(uid string, internal bool) func(c *tsp.Caller) bool {
	return func(c *tsp.Caller) bool { return c != nil && c.GetUid() == uid && c.GetInternal() == internal }
}

// pushBy matches a Push made for uid, never as internal.
func pushBy(uid string) any {
	return mock.MatchedBy(func(in *tsp.PushRequest) bool { return callerIs(uid, false)(in.Caller) })
}

// blockingM2T makes every Magnet2Torrent return torrent once release is
// closed; started is closed when the first call begins.
func blockingM2T(m2t *Magnet2TorrentClientMock, torrent []byte) (started, release chan struct{}) {
	started, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	m2t.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Run(func(mock.Arguments) {
		once.Do(func() { close(started) })
		<-release
	}).Return(&m2tp.Magnet2TorrentReply{Torrent: torrent}, nil)
	return started, release
}

type getAsResult struct {
	res      *Resource
	resolved bool
	err      error
}

func goGetAs(rm *ResourceMap, b []byte, u Uploader) chan getAsResult {
	ch := make(chan getAsResult, 1)
	go func() {
		res, resolved, err := rm.GetAs(context.Background(), b, u)
		ch <- getAsResult{res, resolved, err}
	}()
	return ch
}

// The loaders read torrent-store as internal whoever asks: what the caller
// may see is decided per request (Touch with the caller's own Caller), not
// by what a shared, cached load could read.
func TestLoadersReadAsInternal(t *testing.T) {
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	yes := true
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		load func(rm *ResourceMap) error
	}{
		{"sha1", func(rm *ResourceMap) error {
			_, _, err := rm.GetAs(ctx, []byte(ih), Uploader{uidA, sourceSite})
			return err
		}},
		{"manifest", func(rm *ResourceMap) error {
			_, err := rm.GetManifest(ctx, ih)
			return err
		}},
		{"magnet of a stored hash", func(rm *ResourceMap) error {
			_, _, err := rm.GetAs(ctx, []byte(privateMagnet(t, trA)), Uploader{uidA, sourceSite})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, _ := resourceMapMocks(rm)
			var mu sync.Mutex
			var reqs []interface{ GetCaller() *tsp.Caller }
			record := func(a mock.Arguments) {
				mu.Lock()
				defer mu.Unlock()
				reqs = append(reqs, a.Get(1).(interface{ GetCaller() *tsp.Caller }))
			}
			any3 := []any{mock.Anything, mock.Anything, mock.Anything}
			ts.On("Touch", any3...).Run(record).Return(&tsp.TouchReply{}, nil)
			ts.On("Pull", any3...).Run(record).Return(&tsp.PullReply{Torrent: priv}, nil)
			ts.On("Files", any3...).Run(record).Return(&tsp.FilesReply{
				Name: "probe", Files: []*tsp.FileInfo{{Path: []string{"probe"}, Length: 1024}}, Private: &yes,
			}, nil)

			require.NoError(t, tc.load(rm))

			require.NotEmpty(t, reqs)
			for _, in := range reqs {
				assert.True(t, callerIs("", true)(in.GetCaller()), "%T with Caller %v", in, in.GetCaller())
			}
		})
	}
}

// A loader's Push carries the caller's uid and never internal: a .torrent
// goes with the caller's source, a magnet's trackers or payload as magnet.
func TestLoaderPushesAsCaller(t *testing.T) {
	pub := loadSintel(t)
	withTr := []byte("magnet:?xt=urn:btih:" + mustInfoHash(t, pub) + "&tr=" + url.QueryEscape("http://extra.invalid/announce"))
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, tc := range []struct {
		name   string
		body   []byte
		stored bool
		source string
	}{
		{".torrent", pub, false, sourceRapidAPI},
		{"magnet of a stored hash", withTr, true, sourceMagnet},
		{"unknown magnet", withTr, false, sourceMagnet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, m2t := resourceMapMocks(rm)
			if tc.stored {
				ts.On("Touch", any3...).Return(&tsp.TouchReply{}, nil)
				ts.On("Pull", any3...).Return(&tsp.PullReply{Torrent: pub}, nil)
			} else {
				ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
				m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: pub}, nil)
			}
			ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
				return callerIs(uidA, false)(in.Caller) && in.Source == tc.source && !in.DryRun
			}), mock.Anything).Return(&tsp.PushReply{}, nil).Once()

			_, _, err := rm.GetAs(context.Background(), tc.body, Uploader{uidA, sourceRapidAPI})

			require.NoError(t, err)
			ts.AssertExpectations(t)
		})
	}
}

func TestManifestPrivateFlag(t *testing.T) {
	priv := privateTorrent(t)
	ih := mustInfoHash(t, priv)
	files := []*tsp.FileInfo{{Path: []string{"probe"}, Length: 1024}}
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}

	t.Run("from the manifest", func(t *testing.T) {
		yes := true
		rm := NewTestResourceMap()
		ts, _ := resourceMapMocks(rm)
		ts.On("Files", any3...).Return(&tsp.FilesReply{Name: "probe", Files: files, Private: &yes}, nil)

		r, err := rm.GetManifest(context.Background(), ih)

		require.NoError(t, err)
		assert.True(t, r.Private)
		ts.AssertNotCalled(t, "Pull", any3...)
	})

	// An old store, or a blob stored before the flag: no field is never false.
	t.Run("without the flag: Pull and parse", func(t *testing.T) {
		rm := NewTestResourceMap()
		ts, _ := resourceMapMocks(rm)
		ts.On("Files", any3...).Return(&tsp.FilesReply{Name: "probe", Files: files}, nil)
		ts.On("Touch", any3...).Return(&tsp.TouchReply{}, nil)
		ts.On("Pull", any3...).Return(&tsp.PullReply{Torrent: priv}, nil)

		r, err := rm.GetManifest(context.Background(), ih)

		require.NoError(t, err)
		assert.True(t, r.Private)
		ts.AssertNumberOfCalls(t, "Pull", 1)
	})
}

// A private magnet of an unknown hash with the user's tr= is pushed as
// magnet2torrent returned it, with the caller's uid; the answer keeps none of
// its trackers.
func TestUnknownPrivateMagnetPushedAsIs(t *testing.T) {
	payload := torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}})
	ctx := context.Background()
	rm := NewTestResourceMap()
	ts, m2t := resourceMapMocks(rm)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	m2t.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.Magnet2TorrentReply{Torrent: payload}, nil)
	ts.On("Push", mock.Anything, mock.MatchedBy(func(in *tsp.PushRequest) bool {
		return bytes.Equal(in.Torrent, payload) && callerIs(uidA, false)(in.Caller) &&
			in.Source == "magnet" && !in.DryRun
	}), mock.Anything).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()
	before := counter(t, privateUploadsTotal.WithLabelValues("magnet", "inserted"))

	res, resolved, err := rm.GetAs(ctx, []byte(privateMagnet(t, trA)), Uploader{uidA, "site"})

	require.NoError(t, err)
	assert.True(t, resolved)
	assert.True(t, res.Private)
	assert.NotContains(t, string(res.Torrent), "tracker.example")
	assert.Equal(t, 1.0, counter(t, privateUploadsTotal.WithLabelValues("magnet", "inserted"))-before)
	ts.AssertExpectations(t)
}

// A store that predates private records drops Caller and stores the
// torrent shared, answering without a record outcome: 503, never a 200.
func TestUnknownPrivateMagnetOutdatedStore(t *testing.T) {
	payload := torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}})
	rm := NewTestResourceMap()
	ts, m2t := resourceMapMocks(rm)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	m2t.On("Magnet2Torrent", mock.Anything, mock.Anything, mock.Anything).Return(&m2tp.Magnet2TorrentReply{Torrent: payload}, nil)
	ts.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(&tsp.PushReply{}, nil).Once()
	outdated := privateUploadsTotal.WithLabelValues("magnet", "outdated")
	before := counter(t, outdated)

	_, _, err := rm.GetAs(context.Background(), []byte(privateMagnet(t, trA)), Uploader{uidA, "site"})

	var ae *apiError
	require.True(t, errors.As(err, &ae), "%v", err)
	assert.Equal(t, http.StatusServiceUnavailable, ae.Status)
	assert.Equal(t, codeStoreOutdated, ae.Code)
	assert.Equal(t, 1.0, counter(t, outdated)-before)
}

// rest-api#6's check refuses a bare private magnet before any Push, signed
// in or not: a magnet with no tracker has nothing to store.
func TestBarePrivateMagnetAnyUID(t *testing.T) {
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	for _, u := range []Uploader{{}, {UID: uidA}} {
		t.Run("uid="+u.UID, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, m2t := resourceMapMocks(rm)
			ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
			m2t.On("Magnet2Torrent", any3...).Return(&m2tp.Magnet2TorrentReply{Torrent: resolvedPrivate(t)}, nil)
			ts.On("Push", any3...).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil)

			_, _, err := rm.GetAs(context.Background(), []byte(privateMagnet(t)), u)

			assert.ErrorIs(t, err, errPrivateMagnet)
			ts.AssertNotCalled(t, "Push", any3...)
		})
	}
}

// A private .torrent is pushed by the handler on every request; the loader,
// whose result is shared and cached, never pushes it.
func TestPrivateTorrentNotPushedByLoader(t *testing.T) {
	any3 := []any{mock.Anything, mock.Anything, mock.Anything}
	rm := NewTestResourceMap()
	ts, _ := resourceMapMocks(rm)
	ts.On("Touch", any3...).Return(nil, status.Error(codes.NotFound, "not found"))
	ts.On("Push", any3...).Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil)

	res, _, err := rm.GetAs(context.Background(), privateTorrent(t), Uploader{uidA, "site"})

	require.NoError(t, err)
	assert.True(t, res.Private)
	ts.AssertNotCalled(t, "Push", any3...)
}

// Decision 40: the anonymous creator's refusal does not reach a signed-in waiter.
func TestCallerRefusalNotSharedWithWaiters(t *testing.T) {
	magnet := []byte(privateMagnet(t, trA))
	rm := NewTestResourceMap()
	ts, m2t := resourceMapMocks(rm)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	started, release := blockingM2T(m2t, torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}}))
	ts.On("Push", mock.Anything, pushBy(""), mock.Anything).
		Return(nil, infoErr(codes.PermissionDenied, "torrent-store", "private_needs_account"))
	ts.On("Push", mock.Anything, pushBy(uidA), mock.Anything).
		Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()

	anon := goGetAs(rm, magnet, Uploader{"", "site"})
	<-started // the anonymous request's loader is running under the infohash
	signedIn := goGetAs(rm, magnet, Uploader{uidA, "site"})
	// Time to join the running loader. Too short could only let the signed-in
	// request start a loader of its own and pass without the rerun, never
	// fail with it.
	time.Sleep(100 * time.Millisecond)
	close(release)

	a, s := <-anon, <-signedIn
	var ae *apiError
	require.True(t, errors.As(a.err, &ae), "%v", a.err)
	assert.Equal(t, codePrivateNeedsAccount, ae.Code)
	require.NoError(t, s.err)
	assert.True(t, s.resolved)
	ts.AssertExpectations(t)
}

// The first user's loader resolves and pushes with their uid; the second,
// waiting on it, gets the shared answer, without a push and without
// resolvedHere (task 9 sends it through Verify).
func TestTwoUsersUnknownPrivateMagnetOneResolves(t *testing.T) {
	magnet := []byte(privateMagnet(t, trA))
	rm := NewTestResourceMap()
	ts, m2t := resourceMapMocks(rm)
	ts.On("Touch", mock.Anything, mock.Anything, mock.Anything).Return(nil, status.Error(codes.NotFound, "not found"))
	started, release := blockingM2T(m2t, torrentWithTiers(t, resolvedPrivate(t), [][]string{{trA}}))
	ts.On("Push", mock.Anything, pushBy(uidA), mock.Anything).
		Return(&tsp.PushReply{RecordOutcome: "inserted"}, nil).Once()

	first := goGetAs(rm, magnet, Uploader{uidA, "site"})
	<-started
	second := goGetAs(rm, magnet, Uploader{uidB, "site"})
	time.Sleep(100 * time.Millisecond) // as in TestCallerRefusalNotSharedWithWaiters
	close(release)

	a, b := <-first, <-second
	require.NoError(t, a.err)
	assert.True(t, a.resolved)
	require.NoError(t, b.err)
	assert.NotNil(t, b.res)
	assert.False(t, b.resolved)
	ts.AssertNumberOfCalls(t, "Push", 1)
	ts.AssertExpectations(t)
}

// What pushPrivate answers and counts beyond TestPrivateTorrentUploadAnswers:
// an ErrorInfo reason this version does not know is a store failure, and
// a record outcome it does not know is counted as unchanged.
func TestPushPrivateUnknownAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reply   *tsp.PushReply
		err     error
		status  int
		outcome string
	}{
		{"unknown reason", nil, infoErr(codes.Unavailable, "torrent-store", "reason_from_the_future"), http.StatusServiceUnavailable, "failed"},
		// not taken for a malformed torrent, which has no ErrorInfo
		{"unknown reason, InvalidArgument", nil, infoErr(codes.InvalidArgument, "torrent-store", "reason_from_the_future"), http.StatusServiceUnavailable, "failed"},
		{"unknown outcome", &tsp.PushReply{RecordOutcome: "merged"}, nil, 0, "unchanged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := NewTestResourceMap()
			ts, _ := resourceMapMocks(rm)
			ts.On("Push", mock.Anything, mock.Anything, mock.Anything).Return(tc.reply, tc.err).Once()
			c := privateUploadsTotal.WithLabelValues(sourceSite, tc.outcome)
			before := counter(t, c)

			got, err := rm.pushPrivate(context.Background(), privateTorrent(t), Uploader{uidA, sourceSite})

			if tc.status == 0 {
				require.NoError(t, err)
				assert.Equal(t, tc.reply.RecordOutcome, got)
			} else {
				var ae *apiError
				require.True(t, errors.As(err, &ae), "%v", err)
				assert.Equal(t, tc.status, ae.Status)
				assert.Empty(t, ae.Code)
			}
			assert.Equal(t, 1.0, counter(t, c)-before)
		})
	}
}
