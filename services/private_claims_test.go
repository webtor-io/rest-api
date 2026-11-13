package services

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	uidA = "0123456789abcdef0123456789abcdef01234567"
	uidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// signed is c signed HS256 with tokenTestSecret.
func signed(t *testing.T, c jwt.MapClaims) string {
	t.Helper()
	return signedWith(t, c, tokenTestSecret)
}

func signedWith(t *testing.T, c jwt.MapClaims, secret string) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(secret))
	require.NoError(t, err)
	return tok
}

// hdr carries tok in X-Token.
func hdr(tok string) tokenGetter {
	return tokenGetter{header: map[string]string{"X-Token": tok}}
}

func TestReadClaims(t *testing.T) {
	hour := time.Now().Add(time.Hour).Unix()
	for _, tc := range []struct {
		name   string
		g      tokenGetter
		secret string
		want   callerClaims // compared without raw
	}{
		{"header", hdr(signed(t, jwt.MapClaims{"uid": uidA, "domain": "rapidapi.com", "exp": hour})), tokenTestSecret, callerClaims{UID: uidA, Domain: "rapidapi.com"}},
		{"internal", hdr(signed(t, jwt.MapClaims{"internal": true})), tokenTestSecret, callerClaims{Internal: true}},
		{"query only", queryToken(signed(t, jwt.MapClaims{"uid": uidA})), tokenTestSecret, callerClaims{}},
		{"foreign secret", hdr(signedWith(t, jwt.MapClaims{"uid": uidA}, "other")), tokenTestSecret, callerClaims{}},
		{"expired", hdr(signed(t, jwt.MapClaims{"uid": uidA, "exp": time.Now().Add(-time.Hour).Unix()})), tokenTestSecret, callerClaims{}},
		{"uid upper case", hdr(signed(t, jwt.MapClaims{"uid": strings.ToUpper(uidA)})), tokenTestSecret, callerClaims{}},
		{"uid short", hdr(signed(t, jwt.MapClaims{"uid": uidA[:39]})), tokenTestSecret, callerClaims{}},
		{"internal not bool", hdr(signed(t, jwt.MapClaims{"internal": "true"})), tokenTestSecret, callerClaims{}},
		// jwt v4 verifies HMAC with an empty key: a forged token must not carry a uid.
		{"empty secret", hdr(signedWith(t, jwt.MapClaims{"uid": uidA, "internal": true}, "")), "", callerClaims{}},
	} {
		got := readClaims(tc.g, tc.secret)
		got.raw = nil
		assert.Equal(t, tc.want, got, tc.name)
	}
}

func TestResign(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	rules := map[string]any{"grace": map[string]any{"token": "g"}}
	for _, tc := range []struct {
		name    string
		exp     any // nil: no exp claim
		wantExp int64
	}{
		{"rapidapi 7 days", now.Add(7 * 24 * time.Hour).Unix(), now.Add(24 * time.Hour).Unix()},
		{"short exp kept", now.Add(time.Hour).Unix(), now.Add(time.Hour).Unix()},
		{"vault: no exp", nil, now.Add(24 * time.Hour).Unix()},
		// jwt reads exp 0 as unset, and min(0, now+24h) would be a link that never expires.
		{"exp zero", float64(0), now.Add(24 * time.Hour).Unix()},
	} {
		in := jwt.MapClaims{"uid": uidA, "hash": "stale", "role": "free", "sessionID": "s", "domain": "webtor.io", "rate": "10M", "rules": rules}
		if tc.exp != nil {
			in["exp"] = tc.exp
		}
		c := callerClaims{UID: uidA, raw: in}
		tok, err := c.resign(tokenTestSecret, tokenTestHash, now)
		require.NoError(t, err)
		got, err := parseToken(tok, tokenTestSecret) // now is in 2027, so every exp here is still ahead of the real clock
		require.NoError(t, err)
		assert.Equal(t, tokenTestHash, got["hash"], tc.name)
		assert.Equal(t, uidA, got["uid"], tc.name)
		assert.EqualValues(t, tc.wantExp, got["exp"], tc.name)
		for _, k := range []string{"role", "sessionID", "domain", "rate", "rules"} {
			assert.EqualValues(t, in[k], got[k], tc.name+" "+k)
		}
	}
}

func TestUploaderSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    callerClaims
		want Uploader
	}{
		{"rapidapi", callerClaims{UID: uidA, Domain: "rapidapi.com"}, Uploader{uidA, "rapidapi"}},
		{"site", callerClaims{UID: uidA, Domain: "webtor.io"}, Uploader{uidA, "site"}},
		{"anonymous", callerClaims{}, Uploader{"", "site"}},
	} {
		assert.Equal(t, tc.want, tc.c.uploader(), tc.name)
	}
}
