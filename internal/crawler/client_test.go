package crawler

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"

	crawlerv1 "github.com/joshdurbin/fuck_cars/gen/crawler/v1"
	"github.com/joshdurbin/fuck_cars/internal/domain"
)

type fakeServer struct {
	crawlerv1.UnimplementedCrawlerServiceServer
	lastReq *crawlerv1.FetchArticleRequest
	cid     string
}

func (s *fakeServer) FetchArticle(ctx context.Context, r *crawlerv1.FetchArticleRequest) (*crawlerv1.FetchArticleResponse, error) {
	s.lastReq = r
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-correlation-id"); len(v) > 0 {
			s.cid = v[0]
		}
	}
	return &crawlerv1.FetchArticleResponse{
		RequestedUrl: r.Url, FinalUrl: r.Url, Title: "Hello", Text: "body", HttpStatus: 200,
		PublishedAt: timestamppb.New(time.Date(2026, 10, 12, 21, 30, 0, 0, time.UTC)),
		FetchedAt:   timestamppb.Now(), ExtractionStatus: crawlerv1.ExtractionStatus_EXTRACTION_STATUS_OK,
	}, nil
}

func TestClientRoundTrip(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	fs := &fakeServer{}
	crawlerv1.RegisterCrawlerServiceServer(srv, fs)
	go srv.Serve(lis)
	defer srv.Stop()

	c, err := Dial(lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := WithCorrelationID(context.Background(), "cid-1")
	art, err := c.FetchArticle(ctx, domain.FetchRequest{URL: "https://x/a", Rendering: domain.RenderAuto, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if art.Title != "Hello" || art.ExtractionStatus != domain.ExtractionOK || art.PublishedAt == nil || art.PublishedAt.Year() != 2026 {
		t.Fatalf("bad mapping: %+v", art)
	}
	if fs.lastReq.RenderingPolicy != crawlerv1.RenderingPolicy_RENDERING_POLICY_AUTO || fs.lastReq.Timeout.AsDuration() != 5*time.Second {
		t.Fatalf("bad request mapping: %+v", fs.lastReq)
	}
	if fs.cid != "cid-1" {
		t.Fatalf("correlation id not propagated: %q", fs.cid)
	}
}

func TestUnknownStatusMapsToFetchFailed(t *testing.T) {
	a := articleFromProto(&crawlerv1.FetchArticleResponse{})
	if a.ExtractionStatus != domain.ExtractionFetchFailed {
		t.Fatalf("got %s", a.ExtractionStatus)
	}
}
