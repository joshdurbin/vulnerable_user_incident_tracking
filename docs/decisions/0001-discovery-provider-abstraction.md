# ADR 0001: Discovery providers behind one interface

Status: accepted

## Context
Discovery must pull candidate article URLs from very different sources: search APIs (GDELT), search
feeds (Google News RSS), publisher feeds (RSS/Atom), sitemaps, and HTML index pages that need the Python
crawler. New sources must be addable without code changes, and each must be testable alone.

## Decision
`internal/discovery` defines a `Provider` interface. Each source type is one implementation in its own
package (`gdelt`, `googlenews`, `feed`, `sitemap`, `crawllinks`). A `Registry` maps a
`discovery_sources.source_type` string to a provider. Workflows and the CLI only ever see
`Provider.Discover(ctx, Request) (*Result, error)`, so scheduling, deduplication, persistence and
provenance are written once.

- Search-style providers (GDELT, Google News) take `Request.Queries`; feed-style providers read `Request.Source.URL`.
- Providers return `Candidate`s with raw provider data and never touch the database.
- Normalization and deduplication happen after discovery, in one place (`discovery.Normalize`).
- Providers own their rate limiting, retries and window partitioning (GDELT caps results at 250).
- Libraries, not hand-written parsers: `gofeed` (RSS/Atom), `gopher-parse-sitemap`, `purell` (URL
  normalization), `go-retryablehttp`, `x/time/rate`.

## Verified behavior (live, 2026-10-16)
- GDELT DOC 2.0 `mode=artlist&format=json` returns JSON with `articles[]` (`url, title, seendate, domain,
  language, sourcecountry`), at most 250 per call, no pagination cursor. Narrow windows (start/end datetime)
  are the only way to get more; the adapter splits a window when it hits the cap.
- Google News RSS items link to `news.google.com/rss/articles/CBMi...` redirect IDs. 0 of 64 sampled IDs
  decode offline to a publisher URL. Every item carries `<source url="publisher">`. Resolution therefore
  needs a separate best-effort resolver chain; unresolved items are kept (marked `NeedsResolution`) and are
  never crawled through Google.

## Consequences
Adding a source type means one new package plus one registry line. The cost is a small interface that
fits search APIs and feeds slightly awkwardly (`Queries` vs `Source.URL`); we accept that over per-type plumbing.

## Lesson: retries must go through the provider's rate limiter (found live, 2026-10-16)
GDELT answers `429` with "one request every 5 seconds". The first design shared a retrying HTTP client
(`go-retryablehttp`) that retried 429s after ~2 s, outside the provider's limiter, which broke the rule and got
the client throttled for several minutes. Search providers now use a **non-retrying** client
(`NewPlainHTTPClient`) and retry themselves: each retry waits a backoff and then passes through the same
`rate.Limiter`. A persistent 429 returns `RateLimitedError` and **stops the remaining queries** in that run.
Feed and sitemap providers keep the retrying client because each hits a different origin.

## Known noise
GDELT keyword queries match sports and unrelated stories ("struck out", "hit the market"). Discovery is
recall-oriented; the classification stage filters, but every irrelevant URL costs a fetch and an LLM call.
A cheap title pre-filter before classification is a cost lever to evaluate with real data.
