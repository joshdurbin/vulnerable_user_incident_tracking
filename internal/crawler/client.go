// Package crawler is the Go client for the Python crawler gRPC service.
package crawler

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/durationpb"

	crawlerv1 "github.com/joshdurbin/fuck_cars/gen/crawler/v1"
	"github.com/joshdurbin/fuck_cars/internal/domain"
)

// Fetcher is the interface the pipeline depends on; tests substitute fakes.
type Fetcher interface {
	FetchArticle(ctx context.Context, req domain.FetchRequest) (*domain.Article, error)
}

// Client implements Fetcher over gRPC.
type Client struct {
	conn *grpc.ClientConn
	rpc  crawlerv1.CrawlerServiceClient
}

// Dial connects to the crawler. In-cluster traffic is plaintext; access is
// restricted with NetworkPolicy.
func Dial(addr string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(correlationInterceptor))
	if err != nil {
		return nil, fmt.Errorf("dial crawler %s: %w", addr, err)
	}
	return &Client{conn: conn, rpc: crawlerv1.NewCrawlerServiceClient(conn)}, nil
}

// New wraps an existing client connection (used by tests).
func New(rpc crawlerv1.CrawlerServiceClient) *Client { return &Client{rpc: rpc} }

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) FetchArticle(ctx context.Context, req domain.FetchRequest) (*domain.Article, error) {
	msg := &crawlerv1.FetchArticleRequest{
		Url:             req.URL,
		RenderingPolicy: renderingToProto(req.Rendering),
		SourceId:        req.SourceID,
		CorrelationId:   req.CorrelationID,
	}
	if req.Timeout > 0 {
		msg.Timeout = durationpb.New(req.Timeout)
		// Leave headroom over the crawler's own timeout so it reports the failure.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout+10*time.Second)
		defer cancel()
	}
	resp, err := c.rpc.FetchArticle(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("crawler FetchArticle: %w", err)
	}
	return articleFromProto(resp), nil
}

func renderingToProto(p domain.RenderingPolicy) crawlerv1.RenderingPolicy {
	switch p {
	case domain.RenderAuto:
		return crawlerv1.RenderingPolicy_RENDERING_POLICY_AUTO
	case domain.RenderAlways:
		return crawlerv1.RenderingPolicy_RENDERING_POLICY_ALWAYS
	default:
		return crawlerv1.RenderingPolicy_RENDERING_POLICY_HTTP_ONLY
	}
}

var statusFromProto = map[crawlerv1.ExtractionStatus]domain.ExtractionStatus{
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_OK:                domain.ExtractionOK,
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_NO_CONTENT:        domain.ExtractionNoContent,
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_FETCH_FAILED:      domain.ExtractionFetchFailed,
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_BLOCKED:           domain.ExtractionBlocked,
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_ROBOTS_DISALLOWED: domain.ExtractionRobotsDisallowed,
	crawlerv1.ExtractionStatus_EXTRACTION_STATUS_UNSUPPORTED:       domain.ExtractionUnsupported,
}

func articleFromProto(r *crawlerv1.FetchArticleResponse) *domain.Article {
	a := &domain.Article{
		RequestedURL: r.GetRequestedUrl(), FinalURL: r.GetFinalUrl(), CanonicalURL: r.GetCanonicalUrl(),
		Title: r.GetTitle(), Publisher: r.GetPublisher(), Author: r.GetAuthor(), Text: r.GetText(),
		Description: r.GetDescription(), Language: r.GetLanguage(), HTTPStatus: int(r.GetHttpStatus()),
		ContentType: r.GetContentType(), Rendered: r.GetRendered(), ErrorDetail: r.GetErrorDetail(),
		ExtractionStatus: statusFromProto[r.GetExtractionStatus()],
	}
	if a.ExtractionStatus == "" {
		a.ExtractionStatus = domain.ExtractionFetchFailed
	}
	if r.GetPublishedAt() != nil {
		t := r.GetPublishedAt().AsTime()
		a.PublishedAt = &t
	}
	if r.GetFetchedAt() != nil {
		a.FetchedAt = r.GetFetchedAt().AsTime()
	}
	return a
}
