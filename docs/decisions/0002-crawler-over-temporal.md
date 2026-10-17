# 0002: The Python crawler is a Temporal worker, not a gRPC service

Status: accepted (replaces the gRPC contract in the original design, which has since been removed)

## Context
Milestone 1 followed the original design: a Python gRPC service with a protobuf contract and a Go client.
Temporal now orchestrates the pipeline. A second RPC layer between Go and Python duplicated what
Temporal already provides: retries, timeouts, backoff, a correlation ID, and per-call visibility.
It also added a proto toolchain (`buf`, generated stubs in two languages) to maintain.

## Decision
- The Python crawler is a `temporalio` worker on the `collision-crawl` task queue, registering
  `fetch_article` and `discover_links` activities. Go workflows call them by name.
- Persistence stays in Go. The workflow runs `FindArticle` (Go), `fetch_article` (Python),
  `StoreArticle` (Go). Python has no database access.
- Callers that are not workflows (CLI, crawl-backed discovery providers) go through one-activity
  wrapper workflows (`CrawlFetchWorkflow`, `CrawlLinksWorkflow`) via `crawler.Client`.
- Article text travels through workflow history. A zlib `PayloadCodec`, implemented in both Go
  (`internal/payloadcodec`) and Python (`codec.py`), compresses payloads over 1 KB. The Go test
  decodes a payload produced by the Python codec. Namespace retention bounds how long history is kept;
  Postgres remains the system of record for text (90 days).
- Policy outcomes (blocked by SSRF guard, robots.txt, no content) are returned as an extraction
  status; only bad input is a non-retryable error.

## Consequences
- Good: one orchestration mechanism; the crawler shows up in Temporal history; no generated code.
- Cost: fetching from the CLI now needs a Temporal server, the Python worker and a Go worker.
  Mitigation: `deploy/local/docker-compose.yaml` starts Temporal and the crawler.
- Cost: the Temporal UI and CLI show compressed payloads as opaque until a codec server is added.
- Standalone activities (a newer Temporal feature the Go SDK already exposes) would remove the
  wrapper workflows. Not used yet because the cluster's server version is unverified.
- Limitation: Go-to-Python interoperability is tested end to end (the integration test runs the real
  Python worker). The Python-to-Go direction for large payloads is covered by the shared fixture.
