package services

import (
	"math"
	"sort"

	"github.com/pkg/errors"
)

const (
	maxSubdomains     = 3
	infohashMaxSpread = 1
)

type Subdomains struct {
	nsp *NodesStat
}

func NewSubdomains(nsp *NodesStat) *Subdomains {
	return &Subdomains{
		nsp: nsp,
	}
}

type NodeStatWithScore struct {
	NodeStat
	Score    float64
	Distance int
}

func (s *Subdomains) filterByPool(stats []NodeStatWithScore, pool string) []NodeStatWithScore {
	var res []NodeStatWithScore
	for _, st := range stats {
		for _, p := range st.Pools {
			if pool == p {
				res = append(res, st)
			}
		}
	}
	return res
}

func (s *Subdomains) filterByRole(stats []NodeStatWithScore, role string) []NodeStatWithScore {
	var res []NodeStatWithScore
	for _, st := range stats {
		if st.IsAllowed(role) {
			res = append(res, st)
		}
	}
	return res

}

func (s *Subdomains) filterWithZeroScore(stats []NodeStatWithScore) []NodeStatWithScore {
	var res []NodeStatWithScore
	for _, st := range stats {
		if st.Score != 0 {
			res = append(res, st)
		}
	}
	return res
}

func (s *Subdomains) updateScoreByInfoHash(stats []NodeStatWithScore, infohash string) ([]NodeStatWithScore, error) {
	if len(stats) == 0 {
		return stats, nil
	}
	// Rendezvous over node names — the same ranking torrent-http-proxy
	// computes in distributeByNodeHash (both copies of rendezvousOrder are
	// pinned to one literal test vector). The first node is the one thp
	// will call home, so that is where the client is sent; the runners-up
	// are its fallbacks in order. Until 2026-09-07 both sides cut the hash
	// space into intervals over nodes sorted by name, and a node joining or
	// leaving moved about half the hashes to another node (cold per-node
	// disk caches); with rendezvous only the hashes of that node move.
	names := make([]string, 0, len(stats))
	for _, st := range stats {
		names = append(names, st.Name)
	}
	rank := map[string]int{}
	for i, n := range rendezvousOrder(infohash, names) {
		rank[n] = i
	}
	spread := int(math.Floor(float64(len(stats)) / 2))
	if spread > infohashMaxSpread {
		spread = infohashMaxSpread
	}
	// Distance 0 is home (full score), 1..spread the next runners-up
	// (score halved per step), everything further spread+1.
	for i := range stats {
		d := rank[stats[i].Name]
		if d > spread+1 {
			d = spread + 1
		}
		stats[i].Distance = d
	}
	for i := range stats {
		if stats[i].Distance == 0 {
			continue
		}
		ratio := 1 / float64(stats[i].Distance) / 2
		stats[i].Score = stats[i].Score * ratio
	}
	return stats, nil
}

func (s *Subdomains) getScoredStats(infohash string, pool string, role string) ([]NodeStatWithScore, error) {
	stats, err := s.nsp.Get()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get nodes stat")
	}
	var sc []NodeStatWithScore
	for _, s := range stats {
		if s.Subdomain == "" {
			continue
		}
		sc = append(sc, NodeStatWithScore{
			NodeStat: s,
			Score:    1,
			Distance: -1,
		})
	}
	if len(sc) == 0 {
		return sc, nil
	}
	found := false
	for _, v := range sc {
		for _, vv := range v.Pools {
			if vv == pool {
				found = true
			}
		}
	}
	if !found {
		pool = ""
	}
	return s.getScoredStatsByPoolAndRole(sc, infohash, pool, role)
}

func (s *Subdomains) getScoredStatsByPoolAndRole(sc []NodeStatWithScore, infohash string, pool string, role string) ([]NodeStatWithScore, error) {
	if pool != "" {
		sc = s.filterByPool(sc, pool)
	}
	if role != "" {
		sc = s.filterByRole(sc, role)
	}
	sc, err := s.updateScoreByInfoHash(sc, infohash)
	if err != nil {
		return nil, errors.Wrap(err, "failed to update score by hash")
	}
	sort.Slice(sc, func(i, j int) bool {
		return sc[i].Score > sc[j].Score
	})
	sc = s.filterWithZeroScore(sc)
	return sc, nil
}

func (s *Subdomains) Get(infohash string, pool string, role string) ([]string, error) {
	stats, err := s.getScoredStats(infohash, pool, role)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get sorted nodes stat")
	}
	var res []string
	for _, st := range stats {
		res = append(res, st.Subdomain)
	}
	l := len(res)
	if l > maxSubdomains {
		l = maxSubdomains
	}
	return res[0:l], nil
}
