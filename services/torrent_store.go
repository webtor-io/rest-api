package services

import (
	"fmt"
	"google.golang.org/grpc/credentials/insecure"
	"sync"

	"github.com/urfave/cli"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	ts "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc"
)

type TorrentStore struct {
	cl   ts.TorrentStoreClient
	host string
	port int
	conn *grpc.ClientConn
	err  error
	once sync.Once
}

const (
	torrentStoreHostFlag = "torrent-store-host"
	torrentStorePortFlag = "torrent-store-port"
)

func RegisterTorrentStoreFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   torrentStoreHostFlag,
			Usage:  "torrent store host",
			Value:  "",
			EnvVar: "TORRENT_STORE_SERVICE_HOST, TORRENT_STORE_HOST",
		},
		cli.IntFlag{
			Name:   torrentStorePortFlag,
			Usage:  "torrent store port",
			Value:  50051,
			EnvVar: "TORRENT_STORE_SERVICE_PORT, TORRENT_STORE_PORT",
		},
	)
}

func NewTorrentStore(c *cli.Context) *TorrentStore {
	return &TorrentStore{
		host: c.String(torrentStoreHostFlag),
		port: c.Int(torrentStorePortFlag),
	}
}

func (s *TorrentStore) get() (ts.TorrentStoreClient, error) {
	log.Info("initializing TorrentStoreClient")
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	s.conn = conn
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dial torrent store addr=%v", addr)
	}
	return ts.NewTorrentStoreClient(s.conn), nil
}

func (s *TorrentStore) Get() (ts.TorrentStoreClient, error) {
	s.once.Do(func() {
		s.cl, s.err = s.get()
	})
	return s.cl, s.err
}

func (s *TorrentStore) Close() {
	if s.conn != nil {
		s.conn.Close()
	}
}
