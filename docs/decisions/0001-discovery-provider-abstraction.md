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
