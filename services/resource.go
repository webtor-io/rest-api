package services

import (
	"bytes"
	"context"
	"strings"
	"time"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pkg/errors"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/webtor-io/lazymap"

	m2tp "github.com/webtor-io/magnet2torrent/magnet2torrent"
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
}

type File struct {
	Path   []string
	Size   int64
	Pieces []Hash
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
		magnetTimeout:       3 * time.Minute,
	}
}

func (s *ResourceMap) parseMagnet(b []byte) (*Resource, error) {
	ma, err := metainfo.ParseMagnetUri(string(b))
	if err != nil {
		return nil, err
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

	offset := int64(0)
	for _, f := range i.UpvertedFiles() {
		start, end := offset/i.PieceLength, (offset+f.Length)/i.PieceLength
		path := f.Path
		if len(f.PathUtf8) > 0 {
			path = f.PathUtf8
		}
		r.Files = append(r.Files, &File{
			Path:   append([]string{name}, path...),
			Size:   f.Length,
			Pieces: pieces[start : end+1],
		})
		offset += f.Length
	}
	r.MagnetURI = mi.Magnet(nil, &i).String()
	r.Torrent = b
	return r, nil
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

func (s *ResourceMap) get(ctx context.Context, r *Resource, b []byte) (*Resource, error) {
	ts, err := s.ts.Get()
	if err != nil {
		return nil, err
	}
	found := true
	touchCtx, touchCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
	defer touchCancel()
	_, err = ts.Touch(touchCtx, &tsp.TouchRequest{InfoHash: r.ID})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case gcodes.PermissionDenied:
				return nil, errors.Wrap(err, "forbidden")
			case gcodes.NotFound:
				found = false
			default:
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	switch r.Type {
	case ResourceTypeSha1:
		if !found {
			return nil, errors.Errorf("not found sha1=%v", r.ID)
		}
		pullCtx, pullCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
		defer pullCancel()
		rep, err := ts.Pull(pullCtx, &tsp.PullRequest{InfoHash: r.ID})
		if err != nil {
			return nil, err
		}
		return s.parseTorrent(rep.GetTorrent())

	case ResourceTypeTorrent:
		// Always push: torrent-store merges announces/url-list with whatever
		// already exists, so re-uploading a .torrent with fresher trackers
		// extends the swarm instead of being silently dropped.
		pushCtx, pushCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
		defer pushCancel()
		if _, err := ts.Push(pushCtx, &tsp.PushRequest{Torrent: b}); err != nil {
			return nil, err
		}
		return r, nil

	case ResourceTypeMagnet:
		if found {
			pullCtx, pullCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
			defer pullCancel()
			rep, err := ts.Pull(pullCtx, &tsp.PullRequest{InfoHash: r.ID})
			if err != nil {
				return nil, err
			}
			// Even when the torrent is already cached, push any tr= trackers
			// from the incoming magnet so torrent-store can merge them in.
			if extra := magnetTrackerTorrent(b, rep.GetTorrent()); extra != nil {
				pushCtx, pushCancel := context.WithTimeout(ctx, s.torrentStoreTimeout)
				defer pushCancel()
				_, _ = ts.Push(pushCtx, &tsp.PushRequest{Torrent: extra})
			}
			return s.parseTorrent(rep.GetTorrent())
		}
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
			return nil, err
		}
		// Inject the magnet's tr= trackers before pushing — m2t strips them
		// during DHT metadata exchange, leaving the .torrent trackerless.
		payload := rep.GetTorrent()
		if augmented := magnetTrackerTorrent(b, payload); augmented != nil {
			payload = augmented
		}
		_, err = ts.Push(ctx, &tsp.PushRequest{Torrent: payload})
		if err != nil {
			return nil, err
		}
		return s.parseTorrent(payload)
	}
	return nil, nil
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

func (s *ResourceMap) Get(ctx context.Context, b []byte) (*Resource, error) {
	r, err := s.parse(b)
	if err != nil {
		return nil, err
	}
	return s.LazyMap.Get(r.ID, func() (*Resource, error) {
		return s.get(ctx, r, b)
	})
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
	rep, err := ts.Files(fctx, &tsp.FilesRequest{InfoHash: infohash})
	if err != nil {
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case gcodes.Unimplemented:
				// torrent-store predates the Files RPC — fall back to the full
				// pull+parse path so rest-api stays deployable independently of
				// the store's rollout/rollback.
				return s.Get(ctx, []byte(infohash))
			case gcodes.PermissionDenied:
				// "forbidden" so the web error handler maps to 403, matching get().
				return nil, errors.Wrap(err, "forbidden")
			case gcodes.NotFound:
				return nil, errors.Errorf("not found infoHash=%v", infohash)
			}
		}
		return nil, err
	}
	r := &Resource{
		ID:   infohash,
		Name: rep.GetName(),
		Type: ResourceTypeSha1,
	}
	for _, f := range rep.GetFiles() {
		r.Files = append(r.Files, &File{
			Path: f.GetPath(),
			Size: f.GetLength(),
		})
	}
	return r, nil
}
