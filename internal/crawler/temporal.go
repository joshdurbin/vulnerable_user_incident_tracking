package crawler

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/joshdurbin/vulnerable_user_incident_tracking/internal/domain"
)

// Client implements Fetcher and LinkDiscoverer for callers outside a workflow
// (the CLI, and discovery providers running inside an activity). Each call runs
// a one-activity workflow, so it is retried, time-limited and visible in
// Temporal like everything else. Inside workflows, call the activities directly.
type Client struct {
	c         client.Client
	taskQueue string // queue served by the Go workers that host the wrapper workflows
}

func New(c client.Client, workflowTaskQueue string) *Client {
	return &Client{c: c, taskQueue: workflowTaskQueue}
}

func (c *Client) run(ctx context.Context, wf string, id string, timeout time.Duration, in, out any) error {
	opts := client.StartWorkflowOptions{ID: id, TaskQueue: c.taskQueue, WorkflowExecutionTimeout: timeout}
	run, err := c.c.ExecuteWorkflow(ctx, opts, wf, in)
	if err != nil {
		return fmt.Errorf("start %s: %w", wf, err)
	}
	if err := run.Get(ctx, out); err != nil {
		return fmt.Errorf("%s: %w", wf, err)
	}
	return nil
}

func (c *Client) FetchArticle(ctx context.Context, req domain.FetchRequest) (*domain.Article, error) {
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	var out ArticleResponse
	id := fmt.Sprintf("crawl-fetch-%d", time.Now().UnixNano())
	if err := c.run(ctx, WorkflowFetch, id, 5*timeout+time.Minute, ToFetchRequest(req), &out); err != nil {
		return nil, err
	}
	return out.ToArticle(), nil
}

func (c *Client) DiscoverLinks(ctx context.Context, req domain.DiscoverLinksRequest) (*domain.DiscoverLinksResult, error) {
	d := req.MaxDuration
	if d <= 0 {
		d = 60 * time.Second
	}
	var out DiscoverLinksResponse
	id := fmt.Sprintf("crawl-links-%d", time.Now().UnixNano())
	if err := c.run(ctx, WorkflowLinks, id, 3*d+time.Minute, ToLinksRequest(req), &out); err != nil {
		return nil, err
	}
	return out.ToResult(), nil
}
