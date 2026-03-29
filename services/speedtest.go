package services

import (
	"fmt"
	"math/rand"
	"net/url"
	"strconv"

	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

const (
	speedtestDefaultSize = 10 * 1024 * 1024 // 10MB
)

type SpeedTest struct {
	nsp               *NodesStat
	domain            string
	premiumDomain     string
	apiKey            string
	apiSecret         string
	apiRole           string
	useSubdomains     bool
	subdomainsK8SPool string
}

type SpeedtestResponse struct {
	URL string `json:"url"`
}

func NewSpeedTest(c *cli.Context, nsp *NodesStat) *SpeedTest {
	domain := c.String(exportDomainFlag)
	if domain == "" {
		return nil
	}
	return &SpeedTest{
		nsp:               nsp,
		domain:            domain,
		premiumDomain:     c.String(exportPremiumDomainFlag),
		apiKey:            c.String(exportApiKeyFlag),
		apiSecret:         c.String(exportApiSecretFlag),
		apiRole:           c.String(exportApiRoleFlag),
		useSubdomains:     c.BoolT(exportUseSubdomainsFlag),
		subdomainsK8SPool: c.String(exportSubdomainsK8SPoolFlag),
	}
}

func (s *SpeedTest) getRandomSubdomain(role string) (string, error) {
	stats, err := s.nsp.Get()
	if err != nil {
		return "", errors.Wrap(err, "failed to get nodes stat")
	}
	var candidates []NodeStat
	for _, st := range stats {
		if st.Subdomain == "" {
			continue
		}
		if s.subdomainsK8SPool != "" {
			found := false
			for _, p := range st.Pools {
				if p == s.subdomainsK8SPool {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if role != "" && !st.IsAllowed(role) {
			continue
		}
		candidates = append(candidates, st)
	}
	if len(candidates) == 0 {
		return "", nil
	}
	return candidates[rand.Intn(len(candidates))].Subdomain, nil
}

func (s *SpeedTest) makeToken(g ParamGetter) (string, error) {
	if t := g.Query("token"); t != "" {
		return t, nil
	}
	if t := g.GetHeader("X-Token"); t != "" {
		return t, nil
	}
	if s.apiSecret != "" {
		claims := jwt.MapClaims{}
		if s.apiRole != "" {
			claims["role"] = s.apiRole
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		return token.SignedString([]byte(s.apiSecret))
	}
	return "", nil
}

func (s *SpeedTest) getApiKey(g ParamGetter) string {
	if k := g.Query("api-key"); k != "" {
		return k
	}
	if k := g.GetHeader("X-Api-Key"); k != "" {
		return k
	}
	return s.apiKey
}

func (s *SpeedTest) getRole(g ParamGetter) string {
	tokenStr := g.Query("token")
	if tokenStr == "" {
		tokenStr = g.GetHeader("X-Token")
	}
	if tokenStr != "" && s.apiSecret != "" {
		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method=%v", token.Header["alg"])
			}
			return []byte(s.apiSecret), nil
		})
		if err == nil {
			if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
				if r, ok := claims["role"].(string); ok {
					return r
				}
			}
		}
	}
	return s.apiRole
}

func (s *SpeedTest) GetURL(g ParamGetter) (string, error) {
	du, err := url.Parse(s.domain)
	if err != nil {
		return "", errors.Wrap(err, "failed to parse export domain")
	}

	role := s.getRole(g)
	domain := du.Host

	if s.useSubdomains {
		sub, err := s.getRandomSubdomain(role)
		if err != nil {
			return "", errors.Wrap(err, "failed to get random subdomain")
		}
		if sub != "" {
			domain = sub + "." + domain
		}
	}

	token, err := s.makeToken(g)
	if err != nil {
		return "", errors.Wrap(err, "failed to make token")
	}

	apiKey := s.getApiKey(g)

	q := url.Values{}
	q.Set("size", strconv.Itoa(speedtestDefaultSize))
	if token != "" {
		q.Set("token", token)
	}
	if apiKey != "" {
		q.Set("api-key", apiKey)
	}

	u := url.URL{
		Scheme:   du.Scheme,
		Host:     domain,
		Path:     "/speedtest",
		RawQuery: q.Encode(),
	}

	return u.String(), nil
}
