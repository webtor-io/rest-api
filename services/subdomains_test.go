package services

import (
	"fmt"
	"strings"
	"testing"
)

// Shared with torrent-http-proxy (services/service_location_test.go): the
// rendezvous order of nodes for twelve infohashes, over the three workers
// and over seven nodes. Both services must reproduce it literally — the
// first entry is the node thp routes to and rest-api sends the client to.
var rendezvousVector = []struct {
	hash  string
	three []string
	seven []string
}{
	{"06ab54879d3c8177a1fb822437e95842ec3676c2", []string{"worker63", "worker62", "worker64"}, []string{"n1", "n2", "n4", "n3", "n5", "n6", "n7"}},
	{"a7c8d900b2c0a939f4761a3d037fc72384552741", []string{"worker62", "worker63", "worker64"}, []string{"n4", "n5", "n3", "n2", "n6", "n1", "n7"}},
	{"7fe363e26a18b3f3a35226076826b1deef1f18eb", []string{"worker64", "worker62", "worker63"}, []string{"n6", "n4", "n1", "n7", "n2", "n5", "n3"}},
	{"464c43863b3e5ee59ce41bd64851dc53c662949a", []string{"worker64", "worker62", "worker63"}, []string{"n1", "n6", "n4", "n3", "n5", "n2", "n7"}},
	{"cba4525d3a64c5f2421f23b9eb99fe3630538fa1", []string{"worker62", "worker64", "worker63"}, []string{"n4", "n5", "n3", "n6", "n2", "n1", "n7"}},
	{"331522cead8069425d99a9784e11f202a01c0e01", []string{"worker64", "worker62", "worker63"}, []string{"n3", "n4", "n6", "n5", "n1", "n2", "n7"}},
	{"17c08f81c3614734f7ae21a35920e5d443c3060c", []string{"worker63", "worker64", "worker62"}, []string{"n1", "n6", "n4", "n5", "n2", "n7", "n3"}},
	{"4a849030d6bd024394540939e71314db8d136a9e", []string{"worker62", "worker63", "worker64"}, []string{"n5", "n2", "n6", "n4", "n1", "n3", "n7"}},
	{"e831f36d0dd5794b712fe61644efe9bb205c9549", []string{"worker62", "worker64", "worker63"}, []string{"n6", "n4", "n1", "n2", "n3", "n5", "n7"}},
	{"f67dd54947fe381fe0d7359e634aa57ae82324c0", []string{"worker63", "worker64", "worker62"}, []string{"n5", "n2", "n1", "n6", "n7", "n4", "n3"}},
	{"afb1be62a2f9a9bbe3888e81dee9f18e99e064ac", []string{"worker63", "worker62", "worker64"}, []string{"n3", "n7", "n5", "n1", "n6", "n4", "n2"}},
	{"88dd13204862d23f421c2df7bbf50a8b1c729e3d", []string{"worker63", "worker64", "worker62"}, []string{"n4", "n1", "n5", "n7", "n3", "n6", "n2"}},
}

func TestRendezvousOrderMatchesSharedVector(t *testing.T) {
	three := []string{"worker64", "worker62", "worker63"} // deliberately unsorted
	seven := []string{"n7", "n1", "n6", "n2", "n5", "n3", "n4"}
	for _, v := range rendezvousVector {
		if got := rendezvousOrder(v.hash, three); strings.Join(got, ",") != strings.Join(v.three, ",") {
			t.Errorf("%s over 3: got %v want %v", v.hash[:8], got, v.three)
		}
		if got := rendezvousOrder(v.hash, seven); strings.Join(got, ",") != strings.Join(v.seven, ",") {
			t.Errorf("%s over 7: got %v want %v", v.hash[:8], got, v.seven)
		}
	}
}

// The client is sent to the vector's first node — thp's home for that
// hash — and the runner-up comes second.
func TestSubdomainHomeMatchesProxyNodeHash(t *testing.T) {
	s := &Subdomains{}
	for _, v := range rendezvousVector {
		var stats []NodeStatWithScore
		// Hand the scorer the nodes in the worst order to prove order is irrelevant.
		for _, n := range []string{"worker64", "worker62", "worker63"} {
			stats = append(stats, NodeStatWithScore{NodeStat: NodeStat{Name: n, Subdomain: "abra--" + n}, Score: 1, Distance: -1})
		}
		got, err := s.getScoredStatsByPoolAndRole(stats, v.hash, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Name != v.three[0] || got[1].Name != v.three[1] {
			t.Errorf("hash %s…: rest-api ranks %s, %s; thp's order is %v", v.hash[:8], got[0].Name, got[1].Name, v.three)
		}
	}
}

// The home node keeps its score, the runner-up is halved, the third is
// quartered — so a sick home node has an ordered fallback and the client
// list stays three long.
func TestSubdomainNeighboursFollowHome(t *testing.T) {
	s := &Subdomains{}
	var stats []NodeStatWithScore
	for i := 62; i <= 64; i++ {
		stats = append(stats, NodeStatWithScore{NodeStat: NodeStat{Name: fmt.Sprintf("worker%d", i), Subdomain: "x"}, Score: 1, Distance: -1})
	}
	v := rendezvousVector[0]
	got, err := s.getScoredStatsByPoolAndRole(stats, v.hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d nodes, want 3", len(got))
	}
	want := map[string]float64{v.three[0]: 1, v.three[1]: 0.5, v.three[2]: 0.25}
	for _, g := range got {
		if g.Score != want[g.Name] {
			t.Errorf("%s score %v, want %v", g.Name, g.Score, want[g.Name])
		}
	}
}
