package services

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli"

	"github.com/webtor-io/rest-api/services/uploads"
)

const (
	uploadManifestTokenTTL = time.Minute
	uploadManifestTimeout  = 10 * time.Second // as torrentStoreTimeout
)

// uploadManifestMaxBody caps the seeder's index read into memory: 10 000
// paths of 1024 bytes with room to spare. Its ceiling: encoding/json writes
// <, > and & as \u003c (6 bytes), so an upload whose names are made of them
// can exceed it, and is then a 503 for good. A variable so that a test can
// lower it.
var uploadManifestMaxBody int64 = 32 << 20

// uploadManifestMethod names the index request in the upstream metrics.
const uploadManifestMethod = "UploadManifest"

var errUploadManifestUnavailable = &apiError{Status: http.StatusServiceUnavailable, Msg: "upload manifest unavailable"}

// UploadManifest reads an upload's manifest from its seeder through THP:
// GET <target><id>/ with a token of its own, {hash, internal}. Nothing is
// cached: an upload grows while it is listed.
type UploadManifest struct {
	target  string // scheme, host and path prefix, ending in "/"
	apiKey  string
	secret  string
	cl      *http.Client
	timeout time.Duration
}

// NewUploadManifest is nil without a proxy target: the internal THP when it
// is used and has a host, else the export domain.
func NewUploadManifest(c *cli.Context, cl *http.Client) *UploadManifest {
	var base string
	if host := c.String(torrentHTTPProxyHostFlag); c.Bool(useInternalTorrentHTTPProxyFlag) && host != "" {
		base = "http://" + net.JoinHostPort(host, strconv.Itoa(c.Int(torrentHTTPProxyPortFlag)))
	} else if u, err := url.Parse(c.String(exportDomainFlag)); err == nil && u.Host != "" {
		base = u.Scheme + "://" + u.Host
	}
	if base == "" {
		log.Info("uploads: off, no proxy target")
		return nil
	}
	log.Info("uploads: on")
	return &UploadManifest{
		target:  base + c.String(exportPathPrefixFlag),
		apiKey:  c.String(exportApiKeyFlag),
		secret:  c.String(exportApiSecretFlag),
		cl:      cl,
		timeout: uploadManifestTimeout,
	}
}

// Manifest is the upload id as a Resource. THP's 404 is "not found"; any
// other failure is errUploadManifestUnavailable, never a 404. Neither the
// error nor the log carries the request URL: it holds the token and the key.
func (s *UploadManifest) Manifest(ctx context.Context, id string) (*Resource, error) {
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"hash":     id,
		"internal": true,
		"exp":      time.Now().Add(uploadManifestTokenTTL).Unix(),
	}).SignedString([]byte(s.secret))
	if err != nil {
		log.WithError(err).WithField("upload", id).Warn("upload manifest failed")
		return nil, errUploadManifestUnavailable
	}
	q := url.Values{"token": {tok}}
	if s.apiKey != "" {
		q.Set("api-key", s.apiKey)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// Observed on return: a 200 whose body cannot be used is a failed call too.
	start, outcome := time.Now(), outcomeError
	defer func() { observeUpstream(upstreamTorrentHTTPProxy, uploadManifestMethod, outcome, start) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.target+id+"/?"+q.Encode(), nil)
	var res *http.Response
	if err == nil {
		res, err = s.cl.Do(req)
		outcome = httpOutcome(res, err)
	}
	if err != nil {
		log.WithError(withoutURL(err)).WithField("upload", id).Warn("upload manifest failed")
		return nil, errUploadManifestUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.Errorf("not found upload=%v", id)
	default:
		log.WithFields(log.Fields{"upload": id, "status": res.StatusCode}).Warn("upload manifest failed")
		return nil, errUploadManifestUnavailable
	}
	var m struct {
		Name     string
		UIDs     []string `json:"uids"`
		Reserved *int64
		Files    []struct {
			Path string
			Size int64
		}
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, uploadManifestMaxBody)).Decode(&m); err != nil {
		outcome = httpOutcome(nil, err)
		log.WithError(err).WithField("upload", id).Warn("upload manifest failed")
		return nil, errUploadManifestUnavailable
	}
	// An index carries both lists as arrays, empty ones included. Without
	// them a 200 (null, {}, an error object) is some other answer, and read
	// as an index it would be a live upload of no files and no bytes.
	if m.Files == nil || m.UIDs == nil {
		outcome = outcomeError
		log.WithField("upload", id).Warn("upload manifest failed: not an index")
		return nil, errUploadManifestUnavailable
	}
	r := &Resource{ID: id, Name: m.Name, Type: ResourceTypeSha1, Private: uploads.Owned(id), UIDs: m.UIDs, Reserved: m.Reserved}
	var sum int64
	for _, f := range m.Files {
		p := dropEmptyComponents(strings.Split(f.Path, "/"))
		// The seeder lists files by walking a directory, so every one has a
		// path; a file without one ("", missing, only slashes) means some
		// other answer, and would be a nameless row in the listing.
		if len(p) == 0 {
			outcome = outcomeError
			log.WithField("upload", id).Warn("upload manifest failed: a file without a path")
			return nil, errUploadManifestUnavailable
		}
		// No Pad: an upload has no padding files.
		r.Files = append(r.Files, &File{Path: p, Size: f.Size})
		sum += f.Size
	}
	if r.Reserved == nil { // a list from Vault: what is stored is what it holds
		r.Reserved = &sum
	}
	return r, nil
}
