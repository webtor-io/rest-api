package services

import (
	"regexp"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
)

// callerClaims is who calls, read off a verified X-Token.
type callerClaims struct {
	UID      string        // ^[0-9a-f]{40}$ from a verified X-Token, else ""
	Internal bool          // claim "internal" == true
	Domain   string        // claim "domain"
	raw      jwt.MapClaims // verified X-Token claims; nil without one
}

// Uploader is who a Push is made for and where the upload came from.
type Uploader struct{ UID, Source string }

const (
	sourceSite     = "site"
	sourceRapidAPI = "rapidapi"
	sourceMagnet   = "magnet"
	domainRapidAPI = "rapidapi.com"
	privateLinkTTL = 24 * time.Hour
)

var uidRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// readClaims reads X-Token only, never ?token=; empty secret or any failed
// check → zero value. jwt v4 verifies HMAC with an empty key, so without a
// secret anyone could sign a uid or internal.
func readClaims(g ParamGetter, secret string) callerClaims {
	tok := g.GetHeader("X-Token")
	if secret == "" || tok == "" {
		return callerClaims{}
	}
	raw, err := parseToken(tok, secret)
	if err != nil {
		return callerClaims{}
	}
	c := callerClaims{raw: raw}
	if uid, _ := raw["uid"].(string); uidRe.MatchString(uid) {
		c.UID = uid
	}
	c.Internal, _ = raw["internal"].(bool)
	c.Domain, _ = raw["domain"].(string)
	return c
}

// parseToken is getClaims' HMAC-only verification, shared with
// BaseURLBuilder.getClaims. errorHandler answers 400 on the "failed to
// parse token" prefix.
func parseToken(tokenString, secret string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.Errorf("unexpected signing method=%v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse token")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("failed to validate token")
	}
	return claims, nil
}

// resign copies raw, sets hash=h, uid=c.UID, exp=min(exp, now+privateLinkTTL)
// (now+privateLinkTTL without exp, or with exp 0, which jwt reads as unset),
// signs HS256. internal is dropped: the link goes to the end user. Every
// other claim (rules, rate, role, sessionID, domain) stays.
func (c callerClaims) resign(secret, h string, now time.Time) (string, error) {
	claims := jwt.MapClaims{}
	for k, v := range c.raw {
		claims[k] = v
	}
	delete(claims, "internal")
	claims["hash"] = h
	claims["uid"] = c.UID
	exp, _ := claims["exp"].(float64) // raw comes from parseToken: exp is float64 or absent
	if limit := now.Add(privateLinkTTL).Unix(); exp <= 0 || exp > float64(limit) {
		claims["exp"] = limit
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// uploader: {UID, rapidapi} when Domain == domainRapidAPI, else {UID, site}.
// No request header is read.
func (c callerClaims) uploader() Uploader {
	if c.Domain == domainRapidAPI {
		return Uploader{c.UID, sourceRapidAPI}
	}
	return Uploader{c.UID, sourceSite}
}
