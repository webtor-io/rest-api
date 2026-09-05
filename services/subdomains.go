package services

import (
	"math"
	"sort"
	"strconv"

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
	// Ascending by name — the same order torrent-http-proxy uses in
	// distributeByNodeHash. The two services slice the hash space with the
	// same intervals, so the order is what decides whether a client lands on
	// the node thp calls home. Descending here (until 2026-09-05) sent two
	// thirds of hashes to a node thp considered foreign; thp then fell back
	// to preferLocalNode and re-partitioned the whole space over that node's
	// 30 pods, so only ~10 of them ever got traffic — at 3× the intended
	// load — while the rest sat empty (worker62/64 bimodal, worker63 even).
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Name < stats[j].Name
	})
	hex := infohash[0:5]
	num, err := strconv.ParseInt(hex, 16, 64)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse hex from infohash=%v", infohash)
	}
	num = num * 1000
	total := 1048575 * 1000
	// The top of the space ("fffff…") is >= len*interval once integer
	// division has floored the interval; it belongs to the last node, the
	// same one thp gives it. Defaulting to 0 sent it to the first node.
	t := len(stats) - 1
	interval := int64(total / len(stats))
	for i := 0; i < len(stats); i++ {
		if num < (int64(i)+1)*interval {
			t = i
			break
		}
	}

	spread := int(math.Floor(float64(len(stats)) / 2))
	if spread > infohashMaxSpread {
		spread = infohashMaxSpread
	}
	for i := range stats {
		stats[i].Distance = spread + 1
	}
	for n := -spread; n <= spread; n++ {
		m := t + n
		if m < 0 {
			m = len(stats) + m
		}
		if m >= len(stats) {
			m = m - len(stats)
		}
		d := math.Abs(float64(n))
		stats[m].Distance = int(d)
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
