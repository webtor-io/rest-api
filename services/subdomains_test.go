package services

import (
	"fmt"
	"sort"
	"strconv"
	"testing"
)

// thpHomeNode is torrent-http-proxy's node choice (distributeByNodeHash):
// node names ascending, the 20-bit prefix space cut into equal intervals.
// The subdomain a client is sent to must be that node, or thp's
// preferLocalNode fallback re-partitions the whole space over one node's
// pods and most of them never see traffic.
func thpHomeNode(nodes []string, infohash string) string {
	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)
	num64, _ := strconv.ParseInt(infohash[0:5], 16, 64)
	num := int(num64 * 1000)
	total := 1048575 * 1000
	interval := total / len(sorted)
	for i := range sorted {
		if num < (i+1)*interval {
			return sorted[i]
		}
	}
	return sorted[len(sorted)-1]
}

func TestSubdomainHomeMatchesProxyNodeHash(t *testing.T) {
	nodes := []string{"worker62", "worker63", "worker64"}
	s := &Subdomains{}
	for _, prefix := range []string{"00000", "2aaaa", "55554", "55555", "80000", "aaaa9", "aaaaa", "d0000", "ffff0"} {
		infohash := prefix + "000000000000000000000000000000000000"
		var stats []NodeStatWithScore
		// Hand the scorer the nodes in the worst order to prove it sorts.
		for _, n := range []string{"worker64", "worker62", "worker63"} {
			stats = append(stats, NodeStatWithScore{NodeStat: NodeStat{Name: n, Subdomain: "abra--" + n}, Score: 1, Distance: -1})
		}
		got, err := s.getScoredStatsByPoolAndRole(stats, infohash, "", "")
		if err != nil {
			t.Fatal(err)
		}
		want := thpHomeNode(nodes, infohash)
		if got[0].Name != want {
			t.Errorf("hash %s…: rest-api sends the client to %s, thp's home node is %s", prefix, got[0].Name, want)
		}
	}
}

// The neighbours still come second: the home node scores 1, its two
// neighbours 0.5, so a sick home node has a fallback and the client list
// stays three long.
func TestSubdomainNeighboursFollowHome(t *testing.T) {
	s := &Subdomains{}
	var stats []NodeStatWithScore
	for i := 62; i <= 64; i++ {
		stats = append(stats, NodeStatWithScore{NodeStat: NodeStat{Name: fmt.Sprintf("worker%d", i), Subdomain: "x"}, Score: 1, Distance: -1})
	}
	got, err := s.getScoredStatsByPoolAndRole(stats, "80000000000000000000000000000000000000000", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "worker63" || got[0].Score != 1 || got[1].Score != 0.5 {
		t.Fatalf("got %+v", []string{got[0].Name, fmt.Sprint(got[0].Score), fmt.Sprint(got[1].Score)})
	}
}
