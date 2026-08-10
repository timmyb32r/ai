package yds

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"transferia2-go/internal/config"
	"transferia2-go/internal/metrics"
	"transferia2-go/internal/pqproto"
)

const (
	ydbDatabase      = "/Root"
	ydbSuccess       = 400_000
	maxGRPCMessage   = 128 << 20
	readRequestBytes = 1 << 20
	rawChannelSize   = 64
)

type RawMessage struct {
	Data             []byte
	Codec            pqproto.Codec
	UncompressedSize uint64
	Offset           uint64
}

type RawBatch struct {
	Messages []RawMessage
	Cookie   *pqproto.CommitCookie
}

type Session struct {
	partition int64
	conn      *grpc.ClientConn
	stream    pqproto.PersQueueService_MigrationStreamingReadClient
	sendMu    sync.Mutex
	batches   chan RawBatch
	errors    chan error
	metrics   *metrics.Counters
}

func Open(ctx context.Context, cfg config.PQv1Config, token string, partition int64, counters *metrics.Counters) (*Session, error) {
	endpoint := cfg.ConnectionString
	if endpoint == "" {
		endpoint = cfg.DiscoveryEndpoint
	}
	mainTarget, secure, err := parseEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	proxy, err := discoverProxy(ctx, mainTarget, secure, token)
	if err != nil {
		slog.Warn("PQv1 proxy discovery failed; using configured endpoint", "error", err, "endpoint", mainTarget)
		proxy = mainTarget
	}
	conn, err := dial(proxy, secure)
	if err != nil {
		return nil, err
	}
	streamCtx := outgoingContext(ctx, token)
	stream, err := pqproto.NewPersQueueServiceClient(conn).MigrationStreamingRead(streamCtx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open MigrationStreamingRead: %w", err)
	}
	s := &Session{
		partition: partition, conn: conn, stream: stream,
		batches: make(chan RawBatch, rawChannelSize), errors: make(chan error, 1), metrics: counters,
	}
	init := initMessage(cfg, token, partition)
	if err := s.send(init); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go s.receive(ctx)
	slog.Info("PQv1 session opened", "proxy", proxy, "partition", partition, "topic", cfg.TopicPath)
	return s, nil
}

func initMessage(cfg config.PQv1Config, token string, partition int64) *pqproto.MigrationStreamingReadClientMessage {
	return &pqproto.MigrationStreamingReadClientMessage{
		Request: &pqproto.MigrationStreamingReadClientMessage_InitRequest{InitRequest: &pqproto.InitRequest{
			TopicsReadSettings: []*pqproto.TopicReadSettings{{
				Topic:             cfg.TopicPath,
				PartitionGroupIds: []int64{partition + 1},
			}},
			Consumer:   cfg.ConsumerName,
			ReadParams: &pqproto.ReadParams{MaxReadSize: readRequestBytes},
		}},
		Token: []byte(token),
	}
}

func (s *Session) Batches() <-chan RawBatch { return s.batches }

func (s *Session) Partition() int64 { return s.partition }

func (s *Session) Errors() <-chan error { return s.errors }

func (s *Session) Close() error {
	_ = s.stream.CloseSend()
	return s.conn.Close()
}

func (s *Session) Commit(cookie *pqproto.CommitCookie) error {
	if cookie == nil {
		return nil
	}
	return s.CommitMany([]*pqproto.CommitCookie{cookie})
}

func (s *Session) CommitMany(cookies []*pqproto.CommitCookie) error {
	if len(cookies) == 0 {
		return nil
	}
	copies := make([]*pqproto.CommitCookie, 0, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		copies = append(copies, &pqproto.CommitCookie{
			AssignId: cookie.AssignId, PartitionCookie: cookie.PartitionCookie,
		})
	}
	if len(copies) == 0 {
		return nil
	}
	return s.send(&pqproto.MigrationStreamingReadClientMessage{
		Request: &pqproto.MigrationStreamingReadClientMessage_Commit{Commit: &pqproto.Commit{Cookies: copies}},
	})
}

func (s *Session) send(msg *pqproto.MigrationStreamingReadClientMessage) error {
	s.sendMu.Lock()
	err := s.stream.Send(msg)
	s.sendMu.Unlock()
	return err
}

func (s *Session) receive(ctx context.Context) {
	defer close(s.batches)
	for {
		recvStart := time.Now()
		msg, err := s.stream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				s.reportError(fmt.Errorf("PQv1 receive: %w", err))
			}
			return
		}
		s.metrics.DownloadBusyNanos.Add(uint64(time.Since(recvStart)))
		if msg.Status != 0 && msg.Status != ydbSuccess {
			s.reportError(fmt.Errorf("PQv1 status=%d issues=%v", msg.Status, msg.Issues))
			return
		}
		switch r := msg.Response.(type) {
		case *pqproto.MigrationStreamingReadServerMessage_InitResponse:
			slog.Info("PQv1 initialized", "session_id", r.InitResponse.SessionId, "partition", s.partition)
			if err := s.send(readRequest()); err != nil {
				s.reportError(err)
				return
			}
		case *pqproto.MigrationStreamingReadServerMessage_Assigned:
			a := r.Assigned
			slog.Info("PQv1 partition assigned",
				"partition", a.Partition,
				"assign_id", a.AssignId,
				"read_offset", a.ReadOffset,
				"end_offset", a.EndOffset,
			)
			if int64(a.Partition) != s.partition {
				continue
			}
			req := &pqproto.MigrationStreamingReadClientMessage{Request: &pqproto.MigrationStreamingReadClientMessage_StartRead{StartRead: &pqproto.StartRead{
				Topic: a.Topic, Cluster: a.Cluster, Partition: a.Partition, AssignId: a.AssignId,
				ReadOffset: a.ReadOffset, CommitOffset: a.ReadOffset, VerifyReadOffset: true,
			}}}
			if err := s.send(req); err != nil {
				s.reportError(err)
				return
			}
		case *pqproto.MigrationStreamingReadServerMessage_DataBatch:
			// Keep a read in flight while the previous batch is decompressed,
			// parsed and inserted by downstream stages.
			if err := s.send(readRequest()); err != nil {
				s.reportError(err)
				return
			}
			batch := s.extractBatch(r.DataBatch)
			if len(batch.Messages) == 0 {
				continue
			}
			select {
			case s.batches <- batch:
			case <-ctx.Done():
				return
			}
		case *pqproto.MigrationStreamingReadServerMessage_Release:
			a := r.Release
			if int64(a.Partition) != s.partition {
				continue
			}
			_ = s.send(&pqproto.MigrationStreamingReadClientMessage{Request: &pqproto.MigrationStreamingReadClientMessage_Released{Released: &pqproto.Released{
				Topic: a.Topic, Cluster: a.Cluster, Partition: a.Partition, AssignId: a.AssignId,
			}}})
		}
	}
}

func (s *Session) extractBatch(db *pqproto.DataBatch) RawBatch {
	var out RawBatch
	var compressedBytes, messages uint64
	for _, pd := range db.PartitionData {
		if int64(pd.Partition) != s.partition {
			continue
		}
		out.Cookie = pd.Cookie
		count := 0
		for _, b := range pd.Batches {
			count += len(b.MessageData)
		}
		out.Messages = make([]RawMessage, 0, count)
		for _, b := range pd.Batches {
			for _, m := range b.MessageData {
				compressedBytes += uint64(len(m.Data))
				messages++
				out.Messages = append(out.Messages, RawMessage{Data: m.Data, Codec: m.Codec, UncompressedSize: m.UncompressedSize, Offset: m.Offset})
			}
		}
	}
	if messages != 0 {
		s.metrics.CompressedBytes.Add(compressedBytes)
		s.metrics.Messages.Add(messages)
	}
	return out
}

func (s *Session) reportError(err error) {
	select {
	case s.errors <- err:
	default:
	}
}

func readRequest() *pqproto.MigrationStreamingReadClientMessage {
	return &pqproto.MigrationStreamingReadClientMessage{Request: &pqproto.MigrationStreamingReadClientMessage_Read{Read: &pqproto.Read{}}}
}

func discoverProxy(ctx context.Context, target string, secure bool, token string) (string, error) {
	conn, err := dial(target, secure)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	cctx, cancel := context.WithTimeout(outgoingContext(ctx, token), 10*time.Second)
	defer cancel()
	resp, err := pqproto.NewDiscoveryServiceClient(conn).ListEndpoints(cctx, &pqproto.ListEndpointsRequest{Database: ydbDatabase})
	if err != nil {
		return "", err
	}
	op := resp.Operation
	if op == nil || !op.Ready {
		return "", errors.New("ListEndpoints operation is not ready")
	}
	if op.Status != 0 && op.Status != ydbSuccess {
		return "", fmt.Errorf("ListEndpoints status=%d", op.Status)
	}
	if op.Result == nil {
		return "", errors.New("ListEndpoints has no result")
	}
	var result pqproto.ListEndpointsResult
	if err := proto.Unmarshal(op.Result.Value, &result); err != nil {
		return "", err
	}
	if len(result.Endpoints) == 0 {
		return "", errors.New("ListEndpoints returned no endpoints")
	}
	ep := result.Endpoints[0]
	return net.JoinHostPort(ep.Address, fmt.Sprint(ep.Port)), nil
}

func dial(target string, secure bool) (*grpc.ClientConn, error) {
	var tc credentials.TransportCredentials = insecure.NewCredentials()
	if secure {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			return nil, err
		}
		tc = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
	}
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(tc),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxGRPCMessage), grpc.MaxCallSendMsgSize(maxGRPCMessage)),
		grpc.WithInitialWindowSize(16<<20), grpc.WithInitialConnWindowSize(64<<20),
		grpc.WithReadBufferSize(1<<20), grpc.WithWriteBufferSize(1<<20),
	)
}

func parseEndpoint(s string) (target string, secure bool, err error) {
	if !strings.Contains(s, "://") {
		if _, _, err := net.SplitHostPort(s); err != nil {
			return "", false, err
		}
		return s, false, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", false, err
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("endpoint %q has no host", s)
	}
	switch u.Scheme {
	case "grpc", "http":
		secure = false
	case "grpcs", "https":
		secure = true
	default:
		return "", false, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}
	return u.Host, secure, nil
}

func outgoingContext(ctx context.Context, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ydb-auth-ticket", token,
		"x-ydb-database", ydbDatabase,
		"x-ydb-sdk-build-info", "go-sdk-2021.04.1",
		"user-agent", "grpc-go/1.75.0",
	))
}
