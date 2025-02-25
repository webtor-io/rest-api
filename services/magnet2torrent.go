package services

import (
	"fmt"
	"google.golang.org/grpc/credentials/insecure"
	"sync"

	"github.com/urfave/cli"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	m2t "github.com/webtor-io/magnet2torrent/magnet2torrent"
	"google.golang.org/grpc"
)

type Magnet2Torrent struct {
	cl   m2t.Magnet2TorrentClient
	host string
	port int
	conn *grpc.ClientConn
	err  error
	once sync.Once
}

const (
	magnet2torrentHostFlag = "magnet2torrent-host"
	magnet2torrentPortFlag = "magnet2torrent-port"
)

func RegisterMagnet2TorrentFlags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   magnet2torrentHostFlag,
			Usage:  "magnet2torrent host",
			Value:  "",
			EnvVar: "MAGNET2TORRENT_SERVICE_HOST, MAGNET2TORRENT_HOST",
		},
		cli.IntFlag{
			Name:   magnet2torrentPortFlag,
			Usage:  "magnet2torrent port",
			Value:  50051,
			EnvVar: "MAGNET2TORRENT_SERVICE_PORT, MAGNET2TORRENT_PORT",
		},
	)
}

func NewMagnet2Torrent(c *cli.Context) *Magnet2Torrent {
	return &Magnet2Torrent{
		host: c.String(magnet2torrentHostFlag),
		port: c.Int(magnet2torrentPortFlag),
	}
}

func (s *Magnet2Torrent) get() (m2t.Magnet2TorrentClient, error) {
	log.Info("initializing Magnet2Torrent")
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	s.conn = conn
	if err != nil {
		return nil, errors.Wrapf(err, "failed to dial torrent store addr=%v", addr)
	}
	return m2t.NewMagnet2TorrentClient(s.conn), nil
}

func (s *Magnet2Torrent) Get() (m2t.Magnet2TorrentClient, error) {
	s.once.Do(func() {
		s.cl, s.err = s.get()
	})
	return s.cl, s.err
}

func (s *Magnet2Torrent) Close() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}
