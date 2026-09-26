package services

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/webtor-io/lazymap"
)

// Tokens here are built by hand, not with the jwt library: the library is
// what is under test (dgrijalva/jwt-go gave way to golang-jwt/jwt v4), this
// file has to compile against either, and the shapes that must be refused
// (alg none, a header that lies about the algorithm, padded segments) are
// ones a library will not produce.
//
// rest-api parses a token in two places, and neither is an access check:
// the token is copied into ?token= of every URL it mints whether it parses
// or not, and torrent-http-proxy is what refuses it. The parse only picks
// where the URLs point, by the token's role:
//   - BaseURLBuilder.getClaims (export): a role other than "free" gets the
//     premium domain, and the role picks the download subdomain. With a
//     premium domain configured (production) a refused token yields URLs
//     with no host; without one, the export answers 400.
//   - SpeedTest.getRole (/speedtest): the role picks the subdomain; a
//     refused token falls back to the configured role.

func seg64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func jsonSeg(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return seg64(b)
}

func macSig(h func() hash.Hash, key, input string) string {
	m := hmac.New(h, []byte(key))
	m.Write([]byte(input))
	return seg64(m.Sum(nil))
}

// handToken is header.payload signed with HMAC-SHA256 under key, whatever
// alg the header names.
func handToken(t *testing.T, header, payload any, key string) string {
	t.Helper()
	in := jsonSeg(t, header) + "." + jsonSeg(t, payload)
	return in + "." + macSig(sha256.New, key, in)
}

var (
	hdrHS256 = map[string]any{"alg": "HS256", "typ": "JWT"}
	hdrHS512 = map[string]any{"alg": "HS512", "typ": "JWT"}
	hdrRS256 = map[string]any{"alg": "RS256", "typ": "JWT"}
)

// viewerClaims is what web-ui signs into X-Token (golang-jwt v5,
// RegisteredClaims): an integer exp, the tier as role, its session fields.
// vault signs the same shape without domain.
func viewerClaims(exp any) map[string]any {
	return map[string]any{
		"exp":           exp,
		"role":          "paid",
		"rate":          "20M",
		"sessionID":     "s-parse",
		"domain":        "example.org",
		"agent":         "Mozilla/5.0",
		"remoteAddress": "203.0.113.7",
	}
}

func viewerWith(exp any, change func(map[string]any)) map[string]any {
	c := viewerClaims(exp)
	change(c)
	return c
}

// band2p63 lies between 2^63 - 62135596800 and 2^63: golang-jwt v4 turns
// such a time claim into a time.Time whose seconds wrap into the distant
// past, dgrijalva compared int64s (torrent-http-proxy 418b44d).
const band2p63 = 9.223372e18

type tokenCase struct {
	name  string
	token string
	// fresh mints the token at the moment of use, for one whose verdict
	// depends on the second it is checked in.
	fresh func() string
	// payload is what a taken token's claims come back as; nil: refused.
	payload map[string]any
	// was: what dgrijalva/jwt-go did with it, where golang-jwt v4 differs.
	was string
	// noHeader: a line break, which no HTTP header can carry to rest-api
	// (Go's client refuses to send it; net/http folds a continuation line
	// into a space and answers a bare CR with 400); it only comes as
	// ?token=.
	noHeader bool
}

func (c tokenCase) tok() string {
	if c.fresh != nil {
		return c.fresh()
	}
	return c.token
}

func (c tokenCase) taken() bool { return c.payload != nil }

func (c tokenCase) role() string {
	r, _ := c.payload["role"].(string)
	return r
}

// tokenCases are the tokens every parse site sees, signed for secret.
func tokenCases(t *testing.T, secret string) []tokenCase {
	t.Helper()
	now := time.Now()
	later := now.Add(time.Hour).Unix()
	good := viewerClaims(later)
	goodTok := handToken(t, hdrHS256, good, secret)
	parts := strings.Split(goodTok, ".")

	hs512In := jsonSeg(t, hdrHS512) + "." + jsonSeg(t, good)
	hs512 := hs512In + "." + macSig(sha512.New, secret, hs512In)

	noneIn := jsonSeg(t, map[string]any{"alg": "none", "typ": "JWT"}) + "." + jsonSeg(t, good)

	// An RS256 token under a key of the caller's own.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsIn := jsonSeg(t, hdrRS256) + "." + jsonSeg(t, good)
	digest := sha256.Sum256([]byte(rsIn))
	rsSig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}

	// Every segment padded, the signature taken over the padded input: a
	// valid HMAC, only the encoding is off (the signature always needs a
	// pad: 32 bytes).
	pad := func(s string) string { return s + strings.Repeat("=", (4-len(s)%4)%4) }
	padIn := pad(jsonSeg(t, hdrHS256)) + "." + pad(jsonSeg(t, good))
	padded := padIn + "." + pad(macSig(sha256.New, secret, padIn))

	// web-ui's primary token for a grace-rule stream: bound to a torrent,
	// the inner grace token (no exp) inside rules.
	grace := map[string]any{"rate": "20M", "role": "paid", "hash": tokenTestHash, "kind": "grace"}
	primary := viewerWith(later, func(c map[string]any) {
		c["hash"] = tokenTestHash
		c["rules"] = []any{map[string]any{
			"kind": "grace", "scope": "manifest", "duration_sec": 300,
			"token": handToken(t, hdrHS256, grace, secret),
		}}
	})
	// rapidapi-gateway (dgrijalva StandardClaims): integer exp, the plan
	// as role.
	rapid := map[string]any{"sessionID": "s-rapid", "role": "pro", "rate": "100M", "connections": 10,
		"domain": "rapidapi.com", "exp": later}
	// vault (golang-jwt v5) for its store worker: role "vault", no time
	// claims at all.
	vault := map[string]any{"role": "vault", "sessionID": "", "agent": "", "remoteAddress": ""}
	pastIatNbf := viewerWith(later, func(c map[string]any) {
		c["iat"], c["nbf"] = now.Add(-time.Minute).Unix(), now.Add(-time.Minute).Unix()
	})
	audArray := viewerWith(later, func(c map[string]any) { c["aud"] = []string{"x"} })
	iatBand := viewerWith(later, func(c map[string]any) { c["iat"] = band2p63 })
	nbfBand := viewerWith(later, func(c map[string]any) { c["nbf"] = band2p63 })

	return []tokenCase{
		// Taken.
		{name: "web-ui viewer", token: goodTok, payload: good},
		{name: "web-ui primary with a grace rule", token: handToken(t, hdrHS256, primary, secret), payload: primary},
		{name: "web-ui grace token (no exp)", token: handToken(t, hdrHS256, grace, secret), payload: grace},
		{name: "rapidapi-gateway", token: handToken(t, hdrHS256, rapid, secret), payload: rapid},
		{name: "vault", token: handToken(t, hdrHS256, vault, secret), payload: vault},
		{name: "HS512", token: hs512, payload: good},
		{name: "exp with a fraction", token: handToken(t, hdrHS256, viewerClaims(float64(later)+0.5), secret),
			payload: viewerClaims(float64(later) + 0.5)},
		{name: "exp zero (unset)", token: handToken(t, hdrHS256, viewerClaims(0), secret), payload: viewerClaims(0)},
		{name: "iat and nbf in the past", token: handToken(t, hdrHS256, pastIatNbf, secret), payload: pastIatNbf},
		// rest-api checks no audience, with either library.
		{name: "aud as an array", token: handToken(t, hdrHS256, audArray, secret), payload: audArray},
		// None given: rest-api signs its own, {"role": "free"}, and reads it back.
		{name: "no token", token: "", payload: map[string]any{"role": "free"}},

		// Refused.
		{name: "wrong secret", token: handToken(t, hdrHS256, good, secret+"x")},
		{name: "expired a minute ago", token: handToken(t, hdrHS256, viewerClaims(now.Add(-time.Minute).Unix()), secret)},
		{name: "exp this second", was: "taken", fresh: func() string {
			return handToken(t, hdrHS256, viewerClaims(time.Now().Unix()), secret)
		}},
		{name: "exp not a number", was: "taken", token: handToken(t, hdrHS256, viewerClaims("never"), secret)},
		{name: "exp null", was: "taken", token: handToken(t, hdrHS256, viewerClaims(nil), secret)},
		{name: "iat not a number", was: "taken", token: handToken(t, hdrHS256, viewerWith(later, func(c map[string]any) { c["iat"] = "yesterday" }), secret)},
		{name: "nbf a bool", was: "taken", token: handToken(t, hdrHS256, viewerWith(later, func(c map[string]any) { c["nbf"] = true }), secret)},
		{name: "exp between -1 and 1", was: "taken", token: handToken(t, hdrHS256, viewerClaims(0.5), secret)},
		{name: "exp near 2^63", was: "taken", token: handToken(t, hdrHS256, viewerClaims(band2p63), secret)},
		{name: "iat ahead of rest-api's clock", token: handToken(t, hdrHS256, viewerWith(later, func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() }), secret)},
		{name: "nbf ahead of rest-api's clock", token: handToken(t, hdrHS256, viewerWith(later, func(c map[string]any) { c["nbf"] = now.Add(time.Minute).Unix() }), secret)},
		{name: "alg none, no signature", token: noneIn + "."},
		{name: "alg none, HMAC signature", token: noneIn + "." + macSig(sha256.New, secret, noneIn)},
		{name: "RS256 under the caller's key", token: rsIn + "." + seg64(rsSig)},
		// Key confusion: the header names RS256, the signature is an HMAC
		// under rest-api's secret, as if the secret were the public key.
		{name: "RS256 header, HMAC under the secret", token: handToken(t, hdrRS256, good, secret)},
		{name: "EdDSA header", token: handToken(t, map[string]any{"alg": "EdDSA"}, good, secret)},
		{name: "no alg", token: handToken(t, map[string]any{"typ": "JWT"}, good, secret)},
		{name: "one segment", token: "abc"},
		{name: "two segments", token: parts[0] + "." + parts[1]},
		{name: "four segments", token: goodTok + ".x"},
		{name: "not base64", token: "!!!.@@@.###"},
		{name: "header not JSON", token: seg64([]byte("nope")) + "." + parts[1] + "." + parts[2]},
		{name: "claims not an object", token: handToken(t, hdrHS256, []int{1, 2}, secret)},
		{name: "bearer prefix", token: "Bearer " + goodTok},
		{name: "padded segments", was: "taken", token: padded},

		// Taken now, refused before. Only a holder of the secret can sign
		// these; it could as well leave iat and nbf out, and no minter
		// emits them.
		{name: "iat near 2^63", was: "refused", token: handToken(t, hdrHS256, iatBand, secret), payload: iatBand},
		{name: "nbf near 2^63", was: "refused", token: handToken(t, hdrHS256, nbfBand, secret), payload: nbfBand},
		// encoding/base64 skips CR and LF, so the signature still verifies
		// and the role read is the one the same token has without the
		// break. rest-api never puts the token in a header, only
		// percent-encoded into ?token= of the URLs it mints (checked at
		// every site below), and it did that with the refused ones as well;
		// torrent-http-proxy refuses such a token when the URL is used
		// (418b44d). dgrijalva refused one to three breaks only by its
		// padding arithmetic, and took four.
		{name: "line break after the signature", was: "refused", token: goodTok + "\n", payload: good, noHeader: true},
		{name: "CRLF after the signature", was: "refused", token: goodTok + "\r\n", payload: good, noHeader: true},
		{name: "line break inside the signature", was: "refused", token: parts[0] + "." + parts[1] + "." + parts[2][:10] + "\n" + parts[2][10:], payload: good, noHeader: true},
		{name: "four line breaks after the signature", token: goodTok + "\n\n\n\n", payload: good, noHeader: true},
	}
}

// jsonRoundTrip is what a JSON decoder makes of v: numbers as float64,
// arrays as []any.
func jsonRoundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// tokenDiff logs one observation per site and case. The old and the new
// tree are compared by these lines (go test -v -run TokenParse | grep
// TOKENDIFF); they carry no token or time, only what a caller sees.
func tokenDiff(t *testing.T, site string, c tokenCase, format string, args ...any) {
	t.Helper()
	t.Logf("TOKENDIFF\t%s\t%s\t%s", site, c.name, fmt.Sprintf(format, args...))
}

type tokenGetter struct {
	query, header map[string]string
}

func (g tokenGetter) Param(string) string       { return "" }
func (g tokenGetter) Query(k string) string     { return g.query[k] }
func (g tokenGetter) GetHeader(k string) string { return g.header[k] }
func (g tokenGetter) QueryArray(k string) []string {
	if v, ok := g.query[k]; ok {
		return []string{v}
	}
	return nil
}

func queryToken(tok string) tokenGetter {
	if tok == "" {
		return tokenGetter{}
	}
	return tokenGetter{query: map[string]string{"token": tok}}
}

const tokenTestSecret = "parse-test-secret"

// Export: BaseURLBuilder.getClaims takes and refuses what it did, and a
// taken token's claims come back intact.
func TestTokenParseGetClaims(t *testing.T) {
	for _, c := range tokenCases(t, tokenTestSecret) {
		t.Run(c.name, func(t *testing.T) {
			b := &BaseURLBuilder{g: queryToken(c.tok()), apiSecret: tokenTestSecret, apiRole: "free"}
			claims, err := b.getClaims()
			if err != nil {
				tokenDiff(t, "getClaims", c, "refused: %v", err)
			} else {
				r, _ := claims["role"].(string)
				tokenDiff(t, "getClaims", c, "taken role=%q", r)
			}
			if !c.taken() {
				if err == nil {
					t.Fatalf("taken with claims %v", claims)
				}
				if claims != nil {
					t.Errorf("refused, yet claims %v came back", claims)
				}
				// errorHandler answers 400 on this prefix.
				if !strings.HasPrefix(err.Error(), "failed to parse token") {
					t.Errorf("error %q, want it to start with \"failed to parse token\"", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if want := jsonRoundTrip(t, c.payload); !reflect.DeepEqual(map[string]any(claims), want) {
				t.Errorf("claims %v, want %v", claims, want)
			}
		})
	}
}

// /speedtest: SpeedTest.getRole reads the role off a token it takes and
// falls back to the configured role otherwise.
func TestTokenParseSpeedtestRole(t *testing.T) {
	for _, c := range tokenCases(t, tokenTestSecret) {
		t.Run(c.name, func(t *testing.T) {
			st := &SpeedTest{apiSecret: tokenTestSecret, apiRole: "free"}
			got := st.getRole(queryToken(c.tok()))
			tokenDiff(t, "speedtest.getRole", c, "role=%q", got)
			want := "free"
			if c.taken() {
				want = c.role()
			}
			if got != want {
				t.Errorf("role %q, want %q", got, want)
			}
		})
	}
}

const (
	tokenTestHash    = "08ada5a7a6183aae1e09d831df6748d566095a10"
	tokenTestDomain  = "https://api.example"
	tokenTestPremium = "https://premium.example"
)

// noNetwork answers the cache probe without leaving the process.
type noNetwork struct{}

func (noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
}

// tokenNodes: one download node for every role but free, one for free
// only, so the host of a minted URL says which role rest-api read.
func tokenNodes(t *testing.T) *NodesStat {
	t.Helper()
	ns := &NodesStat{LazyMap: lazymap.New[[]NodeStat](&lazymap.Config{Expire: time.Hour, Capacity: 1})}
	stats := []NodeStat{
		{Name: "w-paid", Subdomain: "paid", Pools: []string{"seeder"}, RolesDenied: []string{"free"}},
		{Name: "w-free", Subdomain: "free", Pools: []string{"seeder"}, RolesAllowed: []string{"free"}},
	}
	if _, err := ns.LazyMap.Get("", func() ([]NodeStat, error) { return stats, nil }); err != nil {
		t.Fatal(err)
	}
	return ns
}

// tokenRouter is rest-api's router for export and /speedtest over a
// one-file torrent, configured as production is (secret, subdomains, and
// a premium domain when premium is set).
func tokenRouter(t *testing.T, premium bool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	prevGin, prevLog := gin.DefaultWriter, log.StandardLogger().Out
	gin.DefaultWriter = io.Discard
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		gin.DefaultWriter = prevGin
		log.SetOutput(prevLog)
	})

	ns := tokenNodes(t)
	ub := &URLBuilder{
		sd:                NewSubdomains(ns),
		cm:                newTestCacheMap(&http.Client{Transport: noNetwork{}}, time.Second),
		domain:            tokenTestDomain,
		apiSecret:         tokenTestSecret,
		apiRole:           "free",
		useSubdomains:     true,
		subdomainsK8SPool: "seeder",
		pathPrefix:        "/",
	}
	st := &SpeedTest{nsp: ns, domain: tokenTestDomain, apiSecret: tokenTestSecret, apiRole: "free",
		useSubdomains: true, subdomainsK8SPool: "seeder"}
	if premium {
		ub.premiumDomain = tokenTestPremium
		st.premiumDomain = tokenTestPremium
	}
	rm := NewTestResourceMap()
	res := &Resource{ID: tokenTestHash, Name: "Sintel", Type: ResourceTypeSha1,
		Files: []*File{{Path: []string{"Sintel", "Sintel.mp4"}, Size: 1000}}}
	if _, err := rm.manifests.Get(tokenTestHash, func() (*Resource, error) { return res, nil }); err != nil {
		t.Fatal(err)
	}
	list := NewList()
	w := &Web{rm: rm, c: list, st: st,
		e: NewExport(NewDownloadExporter(ub), NewStreamExporter(ub, NewTagBuilder(ub, list)))}
	r := newRouter()
	r.Use(w.errorHandler)
	r.GET("/resource/:resource_id/export/:content_id", w.getExport)
	r.GET("/speedtest", w.getSpeedtest)
	return r
}

// tokenGet runs a request with tok in ?token= (header false) or in X-Token.
func tokenGet(t *testing.T, h http.Handler, path, tok string, header bool) (int, map[string]any) {
	t.Helper()
	target := path
	if tok != "" && !header {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		target += sep + "token=" + url.QueryEscape(tok)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if tok != "" && header {
		req.Header.Set("X-Token", tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d, body %q: %v", rec.Code, rec.Body.String(), err)
	}
	return rec.Code, body
}

// collectURLs gathers every url, src and poster in a response body.
func collectURLs(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			if s, ok := vv.(string); ok && (k == "url" || k == "src" || k == "poster") {
				*out = append(*out, s)
				continue
			}
			collectURLs(vv, out)
		}
	case []any:
		for _, vv := range x {
			collectURLs(vv, out)
		}
	}
}

// ownToken is the token rest-api signs when a request brings none.
func ownToken(t *testing.T) string {
	return handToken(t, hdrHS256, map[string]any{"role": "free"}, tokenTestSecret)
}

// checkMintedURL: the URL parses (no raw control character in it), points
// at wantHost, and carries exactly the token that came in, percent-encoded.
func checkMintedURL(t *testing.T, raw, wantHost, wantToken string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Errorf("minted URL does not parse: %v", err)
		return
	}
	if strings.ContainsAny(raw, "\r\n") {
		t.Errorf("minted URL %q holds a raw line break", raw)
	}
	if u.Host != wantHost {
		t.Errorf("host %q, want %q (%s)", u.Host, wantHost, raw)
	}
	if got := u.Query().Get("token"); got != wantToken {
		t.Errorf("token in the URL %q, want %q", got, wantToken)
	}
}

func roleHost(role, base string) string {
	if role == "free" {
		return "free." + base
	}
	return "paid." + base
}

func hostsOf(urls []string) string {
	seen := map[string]bool{}
	for _, raw := range urls {
		if u, err := url.Parse(raw); err == nil {
			seen[u.Host] = true
		} else {
			seen["<unparsable>"] = true
		}
	}
	var hs []string
	for h := range seen {
		hs = append(hs, fmt.Sprintf("%q", h))
	}
	sort.Strings(hs)
	return strings.Join(hs, ",")
}

// deliveries: ?token= for every case, X-Token for those a header can
// carry (web-ui, vault and rapidapi-gateway send the header).
func deliveries(c tokenCase) []bool {
	if c.noHeader || c.token == "" && c.fresh == nil {
		return []bool{false}
	}
	return []bool{false, true}
}

func deliveryName(header bool) string {
	if header {
		return "X-Token"
	}
	return "query"
}

// Export end to end. With a premium domain (production), every token gets
// a 200: a taken one URLs on the host its role picks, a refused one URLs
// with no host. Without one, a refused token is a 400.
func TestTokenParseExport(t *testing.T) {
	cases := tokenCases(t, tokenTestSecret)
	for _, premium := range []bool{true, false} {
		cfg := "premium domain"
		if !premium {
			cfg = "no premium domain"
		}
		h := tokenRouter(t, premium)
		for _, c := range cases {
			for _, header := range deliveries(c) {
				t.Run(cfg+"/"+c.name+"/"+deliveryName(header), func(t *testing.T) {
					tok := c.tok()
					status, body := tokenGet(t, h, "/resource/"+tokenTestHash+"/export/0?types=download,stream", tok, header)
					var urls []string
					collectURLs(body["exports"], &urls)
					tokenDiff(t, "export/"+cfg+"/"+deliveryName(header), c, "status=%d hosts=%s error=%v", status, hostsOf(urls), body["error"])

					if !c.taken() && !premium {
						if status != http.StatusBadRequest {
							t.Errorf("status %d, want 400", status)
						}
						if e, _ := body["error"].(string); !strings.HasPrefix(e, "failed to parse token") {
							t.Errorf("error %q", e)
						}
						return
					}
					if status != http.StatusOK {
						t.Fatalf("status %d, want 200: %v", status, body["error"])
					}
					if len(urls) < 3 { // download, stream, the stream's <source>
						t.Fatalf("urls %v", urls)
					}
					wantHost := ""
					if c.taken() {
						base := "api.example"
						if premium && c.role() != "free" {
							base = "premium.example"
						}
						wantHost = roleHost(c.role(), base)
					}
					wantToken := tok
					if tok == "" {
						wantToken = ownToken(t)
					}
					for _, u := range urls {
						checkMintedURL(t, u, wantHost, wantToken)
					}
				})
			}
		}
	}
}

// /speedtest end to end: always a 200, the subdomain picked by the role of
// a taken token, or by the configured role.
func TestTokenParseSpeedtest(t *testing.T) {
	h := tokenRouter(t, true)
	for _, c := range tokenCases(t, tokenTestSecret) {
		for _, header := range deliveries(c) {
			t.Run(c.name+"/"+deliveryName(header), func(t *testing.T) {
				tok := c.tok()
				status, body := tokenGet(t, h, "/speedtest", tok, header)
				var urls []string
				collectURLs(body, &urls)
				tokenDiff(t, "speedtest/"+deliveryName(header), c, "status=%d hosts=%s", status, hostsOf(urls))
				if status != http.StatusOK {
					t.Fatalf("status %d: %v", status, body["error"])
				}
				role := "free"
				if c.taken() {
					role = c.role()
				}
				wantToken := tok
				if tok == "" {
					wantToken = ownToken(t)
				}
				list, _ := body["urls"].([]any)
				if len(list) != 2 {
					t.Fatalf("urls %v, want standard and premium", body["urls"])
				}
				for _, it := range list {
					m, _ := it.(map[string]any)
					raw, _ := m["url"].(string)
					base := "api.example"
					if m["type"] == "premium" {
						base = "premium.example"
					}
					checkMintedURL(t, raw, roleHost(role, base), wantToken)
				}
			})
		}
	}
}

// The token rest-api signs when a request brings none goes to
// torrent-http-proxy in every URL it mints, so its bytes must not move
// with the library. The literals were produced by the dgrijalva/jwt-go
// build (b65ec31); both the export and /speedtest sign it.
func TestOwnTokenBytes(t *testing.T) {
	const secret = "sign-test-secret"
	for _, c := range []struct {
		role, want string
	}{
		{"free", ownTokenFree},
		{"", ownTokenNoRole},
	} {
		t.Run(fmt.Sprintf("role %q", c.role), func(t *testing.T) {
			ub := &BaseURLBuilder{g: tokenGetter{}, apiSecret: secret, apiRole: c.role}
			got, err := ub.getToken()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("export signs %q, want %q", got, c.want)
			}
			got, err = (&SpeedTest{apiSecret: secret, apiRole: c.role}).makeToken(tokenGetter{})
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("/speedtest signs %q, want %q", got, c.want)
			}
			// And by hand: HS256 over unpadded base64url, keys sorted.
			claims := map[string]any{}
			if c.role != "" {
				claims["role"] = c.role
			}
			if hand := handToken(t, hdrHS256, claims, secret); hand != c.want {
				t.Errorf("by hand %q, want %q", hand, c.want)
			}
		})
	}
}

const (
	ownTokenFree   = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoiZnJlZSJ9.YCnOiS2GNPG_lS9HwkLG4CosSEovpDHkIwQ01ejRTm4"
	ownTokenNoRole = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.PqgHy88CONk9vpNLgbyL-O8GrA_4jvmj8Lz7bHw7SCI"
)
