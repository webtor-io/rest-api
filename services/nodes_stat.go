package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/bytefmt"
	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
	"github.com/urfave/cli"
	"github.com/webtor-io/lazymap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	nodeLabelPrefixFlag = "node-label-prefix"
	nodeIFaceFlag       = "node-iface"
)

func RegisterNodesStatFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   nodeLabelPrefixFlag,
			Usage:  "node label prefix",
			EnvVar: "NODE_LABEL_PREFIX",
			Value:  "webtor.io/",
		},
		cli.StringFlag{
			Name:   nodeIFaceFlag,
			Usage:  "node iface",
			EnvVar: "NODE_IFACE",
			Value:  "eth0",
		},
	)
}

type NodeBandwidth struct {
	High    uint64
	Low     uint64
	Current uint64
}

// type NodeCPU struct {
// 	High    float64
// 	Low     float64
// 	Current float64
// }

type NodeStat struct {
	Name string
	NodeBandwidth
	// NodeCPU
	Pools     []string
	Subdomain string
}

type NodesStat struct {
	lazymap.LazyMap
	pcl         *PromClient
	kcl         *K8SClient
	iface       string
	labelPrefix string
}

func NewNodesStat(c *cli.Context, pcl *PromClient, kcl *K8SClient) *NodesStat {
	return &NodesStat{
		LazyMap: lazymap.New(&lazymap.Config{
			Concurrency: 1,
			Expire:      30 * time.Second,
			ErrorExpire: 15 * time.Second,
			Capacity:    1,
		}),
		pcl:         pcl,
		kcl:         kcl,
		labelPrefix: c.String(nodeLabelPrefixFlag),
		iface:       c.String(nodeIFaceFlag),
	}
}

func (s *NodesStat) get(ctx context.Context) ([]NodeStat, error) {
	ns, err := s.getKubeStats(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get stats from k8s")
	}
	ps, err := s.getPromStats(ctx, ns)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get stats from prom")
	}
	if ps == nil {
		return ns, nil
	}
	return ps, nil
}

// func parseCPUTime(t string) (float64, error) {
// 	d := float64(1)
// 	if strings.HasSuffix(t, "m") {
// 		d = 1000
// 		t = strings.TrimSuffix(t, "m")
// 	}
// 	v, err := strconv.Atoi(t)
// 	if err != nil {
// 		return 0, err
// 	}
// 	return float64(v) / d, nil
// }

func (s *NodesStat) getKubeStats(ctx context.Context) ([]NodeStat, error) {
	cl, err := s.kcl.Get()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get k8s client")
	}
	nodes, err := cl.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get nodes")
	}
	res := []NodeStat{}
	for _, n := range nodes.Items {
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Status == corev1.ConditionTrue && c.Type == corev1.NodeReady {
				ready = true
			}
		}
		if !ready {
			continue
		}
		var bwHigh, bwLow uint64
		subdomain := ""
		// a := n.Status.Allocatable[corev1.ResourceCPU]
		// cpuHigh, err := parseCPUTime(a.String())
		// if err != nil {
		// 	return nil, errors.Wrapf(err, "Failed to parse allocateble cpu value=%v", a.String())
		// }
		// cpuLow := cpuHigh - 1
		if v, ok := n.GetLabels()[fmt.Sprintf("%vbandwidth-high", s.labelPrefix)]; ok {
			bwHigh, err = bytefmt.ToBytes(v)
			if err != nil {
				return nil, errors.Wrapf(err, "Failed to parse bandwidth-high value=%v", v)
			}
		}
		if v, ok := n.GetLabels()[fmt.Sprintf("%vbandwidth-low", s.labelPrefix)]; ok {
			bwLow, err = bytefmt.ToBytes(v)
			if err != nil {
				return nil, errors.Wrapf(err, "Failed to parse bandwidth-low value=%v", v)
			}
		}
		// if v, ok := n.GetLabels()[fmt.Sprintf("%vcpu-high", s.labelPrefix)]; ok {
		// 	cpuHigh, err = parseCPUTime(v)
		// 	if err != nil {
		// 		return nil, errors.Wrapf(err, "Failed to parse cpu-high value=%v", v)
		// 	}
		// }
		// if v, ok := n.GetLabels()[fmt.Sprintf("%vcpu-low", s.labelPrefix)]; ok {
		// 	cpuLow, err = parseCPUTime(v)
		// 	if err != nil {
		// 		return nil, errors.Wrapf(err, "Failed to parse cpu-low value=%v", v)
		// 	}
		// }
		if v, ok := n.GetLabels()[fmt.Sprintf("%vsubdomain", s.labelPrefix)]; ok {
			subdomain = v
		}
		pools := []string{}
		for k, v := range n.GetLabels() {
			if strings.HasPrefix(k, s.labelPrefix) && strings.HasSuffix(k, "pool") && v == "true" {
				pools = append(pools, strings.TrimSuffix(strings.TrimPrefix(k, s.labelPrefix), "-pool"))
			}
		}

		res = append(res, NodeStat{
			Name:      n.Name,
			Subdomain: subdomain,
			NodeBandwidth: NodeBandwidth{
				High: bwHigh,
				Low:  bwLow,
			},
			// NodeCPU: NodeCPU{
			// 	High: cpuHigh,
			// 	Low:  cpuLow,
			// },
			Pools: pools,
		})
	}
	return res, nil
}

func (s *NodesStat) getPromStats(ctx context.Context, ns []NodeStat) ([]NodeStat, error) {
	cl, err := s.pcl.Get()
	if err != nil {
		return nil, errors.Wrap(err, "Failed to get prometheus client")
	}
	if cl == nil {
		return nil, nil
	}
	query := fmt.Sprintf("sum by (pod)(rate(node_network_transmit_bytes_total{device=~\"%s\"}[5m])) * on (pod) group_right kube_pod_info * 8", s.iface)
	val, _, err := cl.Query(ctx, query, time.Now())
	if err != nil {
		return nil, err
	}
	data, ok := val.(model.Vector)
	if !ok {
		return nil, errors.Errorf("Failed to parse response %v", val)
	}
	for _, d := range data {
		for i, n := range ns {
			if string(d.Metric["node"]) == n.Name {
				ns[i].NodeBandwidth.Current = uint64(d.Value)
			}
		}
	}
	// query = "sum by (instance) (irate(node_cpu_seconds_total{mode!=\"idle\"}[5m])) * on(instance) group_left(nodename) (node_uname_info)"
	// val, _, err = cl.Query(ctx, query, time.Now())
	// if err != nil {
	// 	return nil, err
	// }
	// data, ok = val.(model.Vector)
	// if !ok {
	// 	return nil, errors.Errorf("Failed to parse response %v", val)
	// }
	// for _, d := range data {
	// 	for i, n := range ns {
	// 		if n.Name == string(d.Metric["nodename"]) {
	// 			ns[i].NodeCPU.Current = float64(d.Value)
	// 		}
	// 	}
	// }
	return ns, nil
}

func (s *NodesStat) Get(ctx context.Context) ([]NodeStat, error) {
	res, err := s.LazyMap.Get("", func() (interface{}, error) {
		return s.get(ctx)
	})
	if err != nil {
		return nil, err
	}
	return res.([]NodeStat), nil
}
