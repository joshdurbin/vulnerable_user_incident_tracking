// Package search keeps a derived OpenSearch index of incidents and queries it. PostgreSQL stays the
// system of record: the index holds nothing that cannot be rebuilt from it, and a search returns only
// incident IDs, which the API then loads (and filters) from PostgreSQL.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// Index is the concrete index name. A change to the mapping bumps the suffix: the new index is created
// and filled by a full reindex, and the old one is left for an operator to delete.
const Index = "collision-incidents-v1"

// MaxHits bounds how many ranked IDs one search returns.
const MaxHits = 500

// Config locates the cluster.
type Config struct {
	URL      string
	Username string
	Password string
	// InsecureSkipVerify accepts the operator-generated self-signed certificate, as the other
	// consumers of the shared cluster do. There is no CA to verify against.
	InsecureSkipVerify bool
	// Replicas is the replica count for the index. Zero is valid (a single-node cluster).
	Replicas int
}

// Client talks to OpenSearch.
type Client struct {
	api      *opensearchapi.Client
	replicas int
}

// New builds a client. It does not connect: a cluster that is down at start-up must not stop the API.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("search url is empty")
	}
	api, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{
		Addresses: []string{cfg.URL}, Username: cfg.Username, Password: cfg.Password, InsecureSkipVerify: cfg.InsecureSkipVerify,
	}})
	if err != nil {
		return nil, err
	}
	return &Client{api: api, replicas: cfg.Replicas}, nil
}

// EnsureIndex creates the index with its mapping if it does not exist. It is safe to call concurrently.
func (c *Client) EnsureIndex(ctx context.Context) error {
	resp, err := c.api.Indices.Exists(ctx, opensearchapi.IndicesExistsReq{Indices: []string{Index}})
	// A HEAD request has no body, so the client reports a 404 as an error; the status code is the answer.
	switch {
	case resp != nil && resp.StatusCode == http.StatusOK:
		return nil
	case resp != nil && resp.StatusCode == http.StatusNotFound:
		// create it below
	case err != nil:
		return fmt.Errorf("check index: %w", err)
	}
	_, err = c.api.Indices.Create(ctx, opensearchapi.IndicesCreateReq{Index: Index, Body: bytes.NewReader(mapping(c.replicas))})
	if err != nil && !strings.Contains(err.Error(), "resource_already_exists_exception") {
		return fmt.Errorf("create index: %w", err)
	}
	return nil
}

// Apply writes upserts and deletes in one bulk request. A delete of a document that is not there is
// not an error. Any other failed item is.
func (c *Client) Apply(ctx context.Context, upserts []Doc, deletes []string) error {
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for _, d := range upserts {
		_ = enc.Encode(map[string]any{"index": map[string]any{"_index": Index, "_id": d.ID}})
		if err := enc.Encode(d); err != nil {
			return err
		}
	}
	for _, id := range deletes {
		_ = enc.Encode(map[string]any{"delete": map[string]any{"_index": Index, "_id": id}})
	}
	resp, err := c.api.Bulk(ctx, opensearchapi.BulkReq{Body: &b})
	if err != nil && resp == nil {
		return fmt.Errorf("bulk: %w", err)
	}
	if resp != nil && resp.Errors {
		for _, item := range resp.Items {
			for op, r := range item {
				if r.Error == nil || (op == "delete" && r.Status == http.StatusNotFound) {
					continue
				}
				return fmt.Errorf("bulk %s %s: %s: %s", op, r.ID, r.Error.Type, r.Error.Reason)
			}
		}
	}
	return nil
}

// Hit is one ranked result.
type Hit struct {
	ID    string
	Score float32
}

// Search returns incident IDs ranked by relevance. The query is user text: it is passed to a
// simple_query_string, which never raises a syntax error, and to a fuzzy multi_match so a misspelt
// street still finds the incident.
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	body, err := json.Marshal(queryBody(q, limit))
	if err != nil {
		return nil, err
	}
	resp, err := c.api.Search(ctx, &opensearchapi.SearchReq{Indices: []string{Index}, Body: bytes.NewReader(body)})
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	out := make([]Hit, 0, len(resp.Hits.Hits))
	for _, h := range resp.Hits.Hits {
		out = append(out, Hit{ID: h.ID, Score: h.Score})
	}
	return out, nil
}

// Count is the number of documents in the index.
func (c *Client) Count(ctx context.Context) (int64, error) {
	resp, err := c.api.Indices.Count(ctx, &opensearchapi.IndicesCountReq{Indices: []string{Index}})
	if err != nil {
		return 0, fmt.Errorf("count: %w", err)
	}
	return int64(resp.Count), nil
}

// Refresh makes recent writes searchable at once. Tests use it; production relies on the refresh interval.
func (c *Client) Refresh(ctx context.Context) error {
	_, err := c.api.Indices.Refresh(ctx, &opensearchapi.IndicesRefreshReq{Index: []string{Index}})
	return err
}

// Drop deletes the index. Used by a full rebuild after the mapping changed, and by tests.
func (c *Client) Drop(ctx context.Context) error {
	_, err := c.api.Indices.Delete(ctx, opensearchapi.IndicesDeleteReq{Indices: []string{Index}})
	if err != nil && !strings.Contains(err.Error(), "index_not_found_exception") {
		return err
	}
	return nil
}
