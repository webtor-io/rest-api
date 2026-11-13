package services

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	tsp "github.com/webtor-io/torrent-store/proto"
)

// signInStore refuses every Touch the way torrent-store refuses an anonymous
// caller of a private hash.
type signInStore struct {
	tsp.UnimplementedTorrentStoreServer
}

func (signInStore) Touch(context.Context, *tsp.TouchRequest) (*tsp.TouchReply, error) {
	return nil, infoErr(codes.PermissionDenied, "torrent-store", "sign_in")
}

// The ErrorInfo of a refusal survives a real gRPC hop, and the upstream
// interceptor counts the call as refused, not forbidden.
func TestStoreErrorInfoOverWire(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	tsp.RegisterTorrentStoreServer(srv, signInStore{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(upstreamUnaryInterceptor(upstreamTorrentStore)))
	require.NoError(t, err)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := upstreamRequestsTotal.WithLabelValues(upstreamTorrentStore, "Touch", outcomeRefused)
	before := counter(t, c)
	_, err = tsp.NewTorrentStoreClient(conn).Touch(ctx, &tsp.TouchRequest{InfoHash: tokenTestHash, Caller: &tsp.Caller{}})
	reason, ok := errorInfoReason(err)
	assert.True(t, ok)
	assert.Equal(t, "sign_in", reason)
	assert.Equal(t, 1.0, counter(t, c)-before)
}
