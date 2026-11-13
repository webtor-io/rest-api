package services

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pkg/errors"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/webtor-io/lazymap"

	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
)

type ResourceType int

const (
	ResourceTypeHash ResourceType = iota
	ResourceTypeMagnet
	ResourceTypeTorrent
	ResourceTypeSha1
)

type Resource struct {
	ID        string
	Name      string
	Size      int64
	Files     []*File
	Type      ResourceType
	MagnetURI string
	Torrent   []byte
	Private   bool // BEP 27 flag; always known
}

type File struct {
	Path   []string
	Size   int64
	Pieces []Hash
	// Pad marks a BEP 47 padding file: filler the encoder inserted so the
	// next real file starts on a piece boundary. It stays in Files so the
	// index of every file after it (ListItem.Index, /export/<idx>, the
	// Stremio Library's persisted file_idx) is unchanged, but listings and
	// sizes leave it out — it is not content.
	Pad bool
}

// isPadFile reports whether a torrent file is BEP 47 padding: the "p"
// attribute when the encoder set one, else the conventional names —
// ".pad/<length>" (libtorrent, qBittorrent) and "_____padding_file_N_..."
// (BitComet). rel is the path relative to the torrent name.
func isPadFile(attr string, rel []string) bool {
	if strings.Contains(attr, "p") {
		return true
	}
	if len(rel) == 0 {
		return false
	}
	return rel[0] == ".pad" || strings.HasPrefix(rel[len(rel)-1], "_____padding_file")
}

const (
	HashSize int = 20
)

type Hash [HashSize]byte

func splitPieces(buf []byte) []Hash {
	var chunk []byte
	hashes := make([]Hash, 0, len(buf)/HashSize+1)
	for len(buf) >= HashSize {
		chunk, buf = buf[:HashSize], buf[HashSize:]
		var h Hash
		copy(h[:], chunk)
		hashes = append(hashes, h)
	}
	return hashes
}

type ResourceMap struct {
	*lazymap.LazyMap[*Resource]
	manifests           *lazymap.LazyMap[*Resource]
	ts                  TorrentStoreGetter
	m2t                 Magnet2TorrentGetter
	magnetTimeout       time.Duration
	torrentStoreTimeout time.Duration
	// privateRefusals holds, for 10 minutes, the infohashes whose bare magnet
	// resolved to a private torrent and was refused with errPrivateMagnet: a
	// retry would only resolve to the same refusal. Nothing is stored for
	// them, so without it every retry costs a magnet2torrent resolution.
	privateRefusals *lazymap.LazyMap[bool]
}

type TorrentStoreGetter interface {
	Get() (tsp.TorrentStoreClient, error)
}

type Magnet2TorrentGetter interface {
	Get() (m2tp.Magnet2TorrentClient, error)
}

func NewResourceMap(ts TorrentStoreGetter, m2t Magnet2TorrentGetter) *ResourceMap {
	manifests := lazymap.New[*Resource](&lazymap.Config{
		Concurrency: 100,
		Expire:      600 * time.Second,
		Capacity:    1000,
	})
	return &ResourceMap{
		LazyMap: lazymap.New[*Resource](&lazymap.Config{
			Concurrency: 100,
			Expire:      600 * time.Second,
			Capacity:    1000,
		}),
		manifests:           manifests,
		ts:                  ts,
		m2t:                 m2t,
		torrentStoreTimeout: 10 * time.Second,
		// Ten minutes, not three: web-ui offers a "try again for 10 minutes"
		// resolution after the normal 60 s attempt fails, and the caller's
		// context, not this cap, ends the ordinary request. magnet2torrent's
		// own cap matches (maxWait).
		magnetTimeout: 10 * time.Minute,
		privateRefusals: lazymap.New[bool](&lazymap.Config{
			Expire:   10 * time.Minute,
			Capacity: 1000,
		}),
	}
}

func (s *ResourceMap) parseMagnet(b []byte) (*Resource, error) {
	ma, err := metainfo.ParseMagnetUri(string(b))
	if err != nil {
		// url.Parse quotes the whole magnet, tr= passkeys included, and the
		// web error handler logs and answers the error as is (gin's access
		// log prints it too). The reason is enough; the caller has the magnet.
		return nil, withoutURL(err)
	}
	// An all-zero infohash parses, then panics magnet2torrent.
	if ma.InfoHash.IsZero() {
		return nil, errors.New("zero infohash")
	}
	return &Resource{
		ID:   ma.InfoHash.HexString(),
		Type: ResourceTypeMagnet,
	}, nil
}

func (s *ResourceMap) parseTorrent(b []byte) (*Resource, error) {
	br := bytes.NewReader(b)
	mi, err := metainfo.Load(br)
	if err != nil {
		return nil, err
	}
	i, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, err
	}
	name := i.Name
	if i.NameUtf8 != "" {
		name = i.NameUtf8
	}
	r := &Resource{
		ID:   mi.HashInfoBytes().HexString(),
		Name: name,
		Size: i.PieceLength * int64(i.NumPieces()),
		Type: ResourceTypeTorrent,
	}
	pieces := splitPieces(i.Pieces)

	for _, f := range i.UpvertedFiles() {
		// TorrentOffset, not a running sum: v2/hybrid torrents align files
		// to piece boundaries, so the sum drifts after the first file.
		start, end := f.TorrentOffset/i.PieceLength, (f.TorrentOffset+f.Length)/i.PieceLength
		path := f.Path
		if len(f.PathUtf8) > 0 {
			path = f.PathUtf8
		}
		rel := dropEmptyComponents(path)
		r.Files = append(r.Files, &File{
			Path:   append([]string{name}, rel...),
			Size:   f.Length,
			Pieces: pieces[start:min(end+1, int64(len(pieces)))],
			Pad:    isPadFile(f.Attr, rel),
		})
	}
	r.Torrent = b
	r.Private = i.Private != nil && *i.Private
	// A BEP 27 private torrent carries the uploader's passkey in its
	// trackers, and torrent-store keeps one record per infohash that anyone
	// knowing the infohash reads back through here. So the .torrent and the
	// magnet go out without announce, announce-list and url-list, for
	// everyone — the uploader has the original file. torrent-store is always
	// pushed the caller's bytes, never r.Torrent.
	if r.Private {
		mi.Announce, mi.AnnounceList, mi.UrlList = "", nil, nil
		var buf bytes.Buffer
		if err := mi.Write(&buf); err != nil {
			return nil, errors.Wrap(err, "failed to encode private torrent")
		}
		r.Torrent = buf.Bytes()
	}
	r.MagnetURI = mi.Magnet(nil, &i).String()
	return r, nil
}

// dropEmptyComponents removes empty path components from a torrent file
// path. BEP 3 forbids them, but torrents with ("Name", "", "04c.mp3") exist
// ("Isabel Wilkerson - The Warmth of Other Suns", 2026-09-20): kept as is,
// the empty component became a directory with no name that the listing
// could not show or link, the archive asked the proxy for "Name//04c.mp3"
// and got 502s, and the torrent looked dead. Dropping the component gives
// the file the path every client already sends ("Name/04c.mp3"); the
// seeder matches paths after cleaning since 17d9bf1, so it resolves.
func dropEmptyComponents(path []string) []string {
	out := make([]string, 0, len(path))
	for _, c := range path {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func (s *ResourceMap) parse(b []byte) (*Resource, error) {
	if sha1R.Match(b) {
		return &Resource{
			ID:   string(b),
			Type: ResourceTypeSha1,
		}, nil
	} else if strings.HasPrefix(string(b), "magnet:") {
		r, err := s.parseMagnet(b)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse magnet")
		}
		return r, nil
	} else {
		r, err := s.parseTorrent(b)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse torrent")
		}
		return r, nil
	}
}

// internalCaller is how the loaders and getManifest read torrent-store: their
// result is shared between requests and cached, so who may see a private hash
// is asked per request (Touch with that request's Caller), never here.
func internalCaller() *tsp.Caller { return &tsp.Caller{Internal: true} }

// caller is the Caller of a Push made for u: its uid, never internal.
func (u Uploader) caller() *tsp.Caller { return &tsp.Caller{Uid: u.UID} }

// loadNote is what GetAs learns about the loader run behind its answer.
type loadNote struct{ ran, resolved bool } // ran: this request's closure ran; resolved: it took the magnet2torrent branch

func (s *ResourceMap) get(ctx context.Context, r *Resource, b []byte, u Uploader, note *loadNote) (*Resource, error) {
	ts, err := s.ts.Get()
	if err != nil {
		return nil, err
	}
	found := true
	touchCtx, touchCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
	defer touchCancel()
	_, err = ts.Touch(touchCtx, &tsp.TouchRequest{InfoHash: r.ID, Caller: internalCaller()})
	if err != nil {
		// fromStore first, as in getManifest: a refusal is never "not found".
		if err = fromStore(err, false); status.Code(err) != gcodes.NotFound {
			return nil, err
		}
		found = false
	}
	switch r.Type {
	case ResourceTypeSha1:
		if !found {
			return nil, errors.Errorf("not found sha1=%v", r.ID)
		}
		pullCtx, pullCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
		defer pullCancel()
		rep, err := ts.Pull(pullCtx, &tsp.PullRequest{InfoHash: r.ID, Caller: internalCaller()})
		if err != nil {
			return nil, fromStore(err, false)
		}
		return s.parseTorrent(rep.GetTorrent())

	case ResourceTypeTorrent:
		// A private .torrent is pushed by the handler, on every request and
		// with that request's caller: what this returns is shared and cached.
		if r.Private {
			return r, nil
		}
		// Always push: torrent-store merges announces/url-list with whatever
		// already exists, so re-uploading a .torrent with fresher trackers
		// extends the swarm instead of being silently dropped.
		pushCtx, pushCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
		defer pushCancel()
		if _, err := ts.Push(pushCtx, &tsp.PushRequest{Torrent: b, Caller: u.caller(), Source: u.Source}); err != nil {
			return nil, fromStore(err, true)
		}
		s.privateRefusals.Drop(r.ID)
		return r, nil

	case ResourceTypeMagnet:
		if found {
			pullCtx, pullCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
			defer pullCancel()
			rep, err := ts.Pull(pullCtx, &tsp.PullRequest{InfoHash: r.ID, Caller: internalCaller()})
			if err != nil {
				return nil, fromStore(err, false)
			}
			// Even when the torrent is already cached, push any tr= trackers
			// from the incoming magnet so torrent-store can merge them in.
			// Not for a private torrent: torrent-store keeps the first
			// trackers it gets for good, and a magnet needs nothing but the
			// infohash, so its tr= could lock out the real ones. A private
			// torrent's trackers come from an uploaded .torrent.
			if extra := magnetTrackerTorrent(b, rep.GetTorrent()); extra != nil && !isPrivate(rep.GetTorrent()) {
				pushCtx, pushCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
				defer pushCancel()
				_, _ = ts.Push(pushCtx, &tsp.PushRequest{Torrent: extra, Caller: u.caller(), Source: sourceMagnet})
			}
			return s.parseTorrent(rep.GetTorrent())
		}
		// Checked only now that torrent-store lacks the hash, so a .torrent
		// uploaded meanwhile, through any replica, is served instead.
		if st, _ := s.privateRefusals.Status(r.ID); st == lazymap.Done && bareMagnet(r, b) {
			return nil, errPrivateMagnet
		}
		note.resolved = true
		m2t, err := s.m2t.Get()
		if err != nil {
			return nil, err
		}
		magnetCtx, magnetCancel := context.WithTimeout(ctx, s.magnetTimeout)
		defer magnetCancel()
		rep, err := m2t.Magnet2Torrent(magnetCtx, &m2tp.Magnet2TorrentRequest{Magnet: string(b)})
		if err != nil && magnetCtx.Err() != nil {
			return nil, errors.Wrap(err, "magnet timeout")
		} else if err != nil {
			// A magnet2torrent that knows private torrents refuses a bare
			// private magnet itself (private_magnet): the same refusal as
			// the check below, which stays for one that does not.
			err = fromStore(err, true)
			if errors.Is(err, errPrivateMagnet) {
				_, _ = s.privateRefusals.Get(r.ID, func() (bool, error) { return true, nil })
			}
			return nil, err
		}
		payload := rep.GetTorrent()
		private := isPrivate(payload)
		magnetResolvedTotal.WithLabelValues(strconv.FormatBool(private)).Inc()
		// A private torrent works only through trackers (BEP 27 rules out DHT
		// and PEX). A bare magnet would be stored trackerless and served that
		// way to every later magnet for the hash, so nothing is pushed and
		// the caller is told to upload the .torrent or bring a tracker.
		//
		// This is decided before torrent-store's stoplist and abuse gates,
		// which only Push applies: a blocked private hash is answered 422
		// here and 403 once its .torrent is uploaded. Accepted: nothing is
		// stored or served either way.
		if private && bareMagnet(r, b) {
			_, _ = s.privateRefusals.Get(r.ID, func() (bool, error) { return true, nil })
			return nil, errPrivateMagnet
		}
		// A private magnet with tr= comes back from magnet2torrent with them
		// as one tier, and goes to torrent-store as is, for this request's
		// caller: their record is made from it.
		if private {
			if _, err := s.pushPrivate(ctx, payload, Uploader{u.UID, sourceMagnet}); err != nil {
				return nil, err
			}
			return s.parseTorrent(payload)
		}
		// Inject the magnet's tr= trackers before pushing — m2t strips them
		// during DHT metadata exchange, leaving the .torrent trackerless.
		if augmented := magnetTrackerTorrent(b, payload); augmented != nil {
			payload = augmented
		}
		_, err = ts.Push(ctx, &tsp.PushRequest{Torrent: payload, Caller: u.caller(), Source: sourceMagnet})
		if err != nil {
			return nil, fromStore(err, true)
		}
		s.privateRefusals.Drop(r.ID)
		return s.parseTorrent(payload)
	}
	return nil, nil
}

// pushPrivate pushes the bytes of a private torrent for u and returns the
// store's record outcome. Never dry_run. A refusal by rule (ErrorInfo, or
// PermissionDenied: the stoplist) is answered as the store said; anything
// else, or a store that predates records (no outcome), is a 503.
func (s *ResourceMap) pushPrivate(ctx context.Context, torrent []byte, u Uploader) (outcome string, err error) {
	label := "failed"
	defer func() { privateUploadsTotal.WithLabelValues(u.Source, label).Inc() }()
	ts, err := s.ts.Get()
	if err != nil {
		return "", errStoreUnavailable
	}
	pushCtx, cancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
	defer cancel()
	rep, err := ts.Push(pushCtx, &tsp.PushRequest{Torrent: torrent, Caller: u.caller(), Source: u.Source})
	if err != nil {
		// An ErrorInfo reason this version does not know reads as a status
		// without one: fromStore leaves it as it came, and it is a 503.
		var ae *apiError
		if err = fromStore(err, true); errors.As(err, &ae) || status.Code(err) == gcodes.PermissionDenied {
			label = "refused"
			return "", err
		}
		return "", errStoreUnavailable
	}
	switch outcome = rep.GetRecordOutcome(); outcome {
	case "":
		label = "outdated"
		return "", errStoreOutdated
	case "inserted", "updated":
		label = outcome
	default:
		label = "unchanged"
	}
	if mi, err := metainfo.Load(bytes.NewReader(torrent)); err == nil {
		s.privateRefusals.Drop(mi.HashInfoBytes().HexString())
	}
	return outcome, nil
}

// touch asks torrent-store whether c may read private torrent h: nil only for
// access=allowed. One Touch per call and nothing remembered (S15): a deleted
// record refuses the very next request. A reply without access comes from a
// store that predates private records and decides nothing.
func (s *ResourceMap) touch(ctx context.Context, h string, c *tsp.Caller) error {
	outcome := "unavailable"
	defer func() { privateLookupsTotal.WithLabelValues(outcome).Inc() }()
	ts, err := s.ts.Get()
	if err != nil {
		return errStoreUnavailable
	}
	touchCtx, cancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
	defer cancel()
	rep, err := ts.Touch(touchCtx, &tsp.TouchRequest{InfoHash: h, Caller: c})
	if err == nil {
		if rep.GetAccess() == "allowed" {
			outcome = "allowed"
			return nil
		}
		outcome = "outdated"
		return errStoreOutdated
	}
	switch reason, _ := errorInfoReason(err); reason {
	case reasonSignIn, reasonNoRecord:
		outcome = "refused"
		return fromStore(err, false)
	case codePrivateUnavailable:
		outcome = "disabled"
		return fromStore(err, false)
	}
	return errStoreUnavailable
}

var (
	errStoreUnavailable = &apiError{Status: http.StatusServiceUnavailable, Msg: "torrent store unavailable"}
	errStoreOutdated    = &apiError{Status: http.StatusServiceUnavailable, Code: codeStoreOutdated, Msg: "torrent store is outdated"}
)

// errPrivateMagnet refuses a magnet with no tracker (bareMagnet) for a BEP 27
// private torrent that is not stored yet: 422 private_magnet. web-ui keys on
// that status; the text is for API callers.
var errPrivateMagnet = &apiError{
	Status: http.StatusUnprocessableEntity,
	Code:   codePrivateMagnet,
	Msg:    "private torrent: this magnet has no tracker; upload the .torrent file or use a magnet that includes your tracker",
}

// magnetTrackerTorrent builds a synthetic torrent that carries the
// magnet's tr= trackers but reuses base's info dict (so the infohash
// stays identical). Returns nil if the magnet has no trackers or parsing
// fails — torrent-store does the actual merge.
func magnetTrackerTorrent(magnetURI []byte, base []byte) []byte {
	ma, err := metainfo.ParseMagnetUri(string(magnetURI))
	if err != nil || len(ma.Trackers) == 0 {
		return nil
	}
	mi, err := metainfo.Load(bytes.NewReader(base))
	if err != nil {
		return nil
	}
	tiers := make(metainfo.AnnounceList, 0, len(ma.Trackers))
	for _, t := range ma.Trackers {
		tiers = append(tiers, []string{t})
	}
	mi.AnnounceList = tiers
	mi.Announce = ma.Trackers[0]
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		return nil
	}
	return buf.Bytes()
}

// bareMagnet reports whether r is a magnet with no tracker in b: no tr=, or
// only empty or blank ones ("&tr=" left unfilled), which announce nowhere.
// It decides only the private-torrent refusal; a public magnet's tr= values
// are pushed as they are.
func bareMagnet(r *Resource, b []byte) bool {
	return r.Type == ResourceTypeMagnet && len(magnetTier(b)) == 0
}

// magnetTier is the magnet's tr= values, trimmed, blanks dropped, in magnet order; nil if it does not parse.
func magnetTier(b []byte) []string {
	ma, err := metainfo.ParseMagnetUri(string(b))
	if err != nil {
		return nil
	}
	var tier []string
	for _, t := range ma.Trackers {
		if t = strings.TrimSpace(t); t != "" {
			tier = append(tier, t)
		}
	}
	return tier
}

// isPrivate reports whether torrent's info dict sets the BEP 27 private flag.
func isPrivate(torrent []byte) bool {
	mi, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		return false
	}
	i, err := mi.UnmarshalInfo()
	return err == nil && i.Private != nil && *i.Private
}

func (s *ResourceMap) Get(ctx context.Context, b []byte) (*Resource, error) {
	res, _, err := s.GetAs(ctx, b, Uploader{Source: sourceSite})
	return res, err
}

// GetAs loads b; resolvedHere reports that this request's loader resolved an unknown magnet and pushed it with u.
func (s *ResourceMap) GetAs(ctx context.Context, b []byte, u Uploader) (res *Resource, resolvedHere bool, err error) {
	r, err := s.parse(b)
	if err != nil {
		return nil, false, err
	}
	// The closure may run on another request's goroutine (lazymap lets a
	// waiter run it while its creator waits for a slot); note is read only
	// after LazyMap.Get returns.
	note := &loadNote{}
	res, err = s.LazyMap.Get(r.ID, func() (*Resource, error) {
		note.ran = true
		return s.get(ctx, r, b, u, note)
	})
	// Requests for one infohash share a loader, so this one may carry a
	// refusal made for whoever started it: a bare magnet's, or the store's
	// answer to that caller. Decision 40: a request whose own loader did not
	// run is run again on its own; a bare one gets the remembered refusal.
	var ae *apiError
	if !note.ran && errors.As(err, &ae) {
		note = &loadNote{ran: true}
		res, err = s.get(ctx, r, b, u, note)
	}
	return res, note.resolved, err
}

// GetManifest returns a lightweight Resource (ID + Name + Files, no piece
// hashes / torrent bytes / magnet URI) for listing and export. It is fed by
// the torrent-store Files RPC, which serves a precomputed, multi-level-cached
// manifest — so listing a torrent with thousands of files no longer transfers
// and parses the full .torrent on every request. infohash must be a hex
// SHA1; magnet/torrent inputs are not accepted here (they only reach the
// store-and-resolve POST path, which uses Get).
func (s *ResourceMap) GetManifest(ctx context.Context, infohash string) (*Resource, error) {
	return s.manifests.Get(infohash, func() (*Resource, error) {
		return s.getManifest(ctx, infohash)
	})
}

func (s *ResourceMap) getManifest(ctx context.Context, infohash string) (*Resource, error) {
	ts, err := s.ts.Get()
	if err != nil {
		return nil, err
	}
	fctx, cancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
	defer cancel()
	rep, err := ts.Files(fctx, &tsp.FilesRequest{InfoHash: infohash, Caller: internalCaller()})
	if err != nil {
		// fromStore ahead of the code: a refusal with ErrorInfo comes back as
		// an apiError, which carries no gRPC code, so whatever code it was
		// sent with it never becomes the 404 of an unknown hash. A
		// PermissionDenied without it comes back marked forbidden (403).
		err = fromStore(err, false)
		switch status.Code(err) {
		case gcodes.Unimplemented:
			// torrent-store predates the Files RPC — fall back to the full
			// pull+parse path so rest-api stays deployable independently of
			// the store's rollout/rollback.
			return s.Get(ctx, []byte(infohash))
		case gcodes.NotFound:
			return nil, errors.Errorf("not found infoHash=%v", infohash)
		}
		return nil, err
	}
	// A store that predates the flag, or a manifest cached before it, says
	// nothing: that never means public, so the torrent itself is read.
	if rep.Private == nil {
		return s.Get(ctx, []byte(infohash))
	}
	r := &Resource{
		ID:      infohash,
		Name:    rep.GetName(),
		Type:    ResourceTypeSha1,
		Private: *rep.Private,
	}
	for _, f := range rep.GetFiles() {
		// Same normalisation as parseTorrent: the manifest is the path
		// every listing takes, so an empty component must go here too.
		p := dropEmptyComponents(f.GetPath())
		var rel []string
		if len(p) > 1 {
			rel = p[1:] // the manifest carries no attrs; names must do
		}
		r.Files = append(r.Files, &File{
			Path: p,
			Size: f.GetLength(),
			Pad:  isPadFile("", rel),
		})
	}
	return r, nil
}
