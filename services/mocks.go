package services

import (
	"context"
	"time"

	"github.com/pkg/errors"

	"github.com/stretchr/testify/mock"
	m2tp "github.com/webtor-io/magnet2torrent/proto"
	tsp "github.com/webtor-io/torrent-store/proto"
	"google.golang.org/grpc"
)

type TorrentStoreClientMock struct {
	mock.Mock
}

func (s *TorrentStoreClientMock) Push(ctx context.Context, in *tsp.PushRequest, opts ...grpc.CallOption) (*tsp.PushReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.PushReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) Pull(ctx context.Context, in *tsp.PullRequest, opts ...grpc.CallOption) (*tsp.PullReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.PullReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) Touch(ctx context.Context, in *tsp.TouchRequest, opts ...grpc.CallOption) (*tsp.TouchReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.TouchReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) Files(ctx context.Context, in *tsp.FilesRequest, opts ...grpc.CallOption) (*tsp.FilesReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.FilesReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) Fingerprint(ctx context.Context, in *tsp.FingerprintRequest, opts ...grpc.CallOption) (*tsp.FingerprintReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.FingerprintReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) GetPrivateRecord(ctx context.Context, in *tsp.GetPrivateRecordRequest, opts ...grpc.CallOption) (*tsp.GetPrivateRecordReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.GetPrivateRecordReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) ListPrivateRecords(ctx context.Context, in *tsp.ListPrivateRecordsRequest, opts ...grpc.CallOption) (*tsp.ListPrivateRecordsReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.ListPrivateRecordsReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) DeletePrivateRecord(ctx context.Context, in *tsp.DeletePrivateRecordRequest, opts ...grpc.CallOption) (*tsp.DeletePrivateRecordReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.DeletePrivateRecordReply)
	return r, args.Error(1)
}
func (s *TorrentStoreClientMock) DeletePrivateRecordsByUser(ctx context.Context, in *tsp.DeletePrivateRecordsByUserRequest, opts ...grpc.CallOption) (*tsp.DeletePrivateRecordsReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*tsp.DeletePrivateRecordsReply)
	return r, args.Error(1)
}

type TorrentStoreMock struct {
	m *TorrentStoreClientMock
}

func NewTorrentStoreMock() *TorrentStoreMock {
	return &TorrentStoreMock{
		m: &TorrentStoreClientMock{},
	}
}

func (s *TorrentStoreMock) Get() (tsp.TorrentStoreClient, error) {
	return s.m, nil
}

type Magnet2TorrentClientMock struct {
	mock.Mock
	sleep time.Duration
}

func (s *Magnet2TorrentClientMock) Magnet2Torrent(ctx context.Context, in *m2tp.Magnet2TorrentRequest, opts ...grpc.CallOption) (*m2tp.Magnet2TorrentReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*m2tp.Magnet2TorrentReply)
	time.Sleep(s.sleep)
	if t, ok := ctx.Deadline(); ok {
		if time.Now().After(t) {
			return nil, errors.Errorf("timeout")
		}

	}
	return r, args.Error(1)
}
func (s *Magnet2TorrentClientMock) Verify(ctx context.Context, in *m2tp.VerifyRequest, opts ...grpc.CallOption) (*m2tp.VerifyReply, error) {
	args := s.Called(ctx, in, opts)
	r, _ := args.Get(0).(*m2tp.VerifyReply)
	return r, args.Error(1)
}

type Magnet2TorrentMock struct {
	m *Magnet2TorrentClientMock
}

func NewMagnet2TorrentMock() *Magnet2TorrentMock {
	return &Magnet2TorrentMock{
		m: &Magnet2TorrentClientMock{},
	}
}

func (s *Magnet2TorrentMock) Get() (m2tp.Magnet2TorrentClient, error) {
	return s.m, nil
}
