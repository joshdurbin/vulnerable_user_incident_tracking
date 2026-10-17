# 0003: No Redis and no OpenSearch

Status: accepted

## Context
The plan reserved Redis (cached statistics, possibly shared rate limits) and OpenSearch (full-text search)
because the cluster offers them. Nothing built so far uses either.

## Decision
Neither is a dependency.

- **Redis.** Statistics are aggregate queries over incidents, a table that grows by tens of rows a day.
  They run in milliseconds on PostgreSQL and the filter columns are indexed. A cache would add a failure
  mode and invalidation rules for no measurable gain. Politeness limits (per domain, per provider) live in
  the workers that make the requests, and Temporal task-queue rate limits bound the rest. Workflow state,
  scheduling and retries are Temporal's job.
- **OpenSearch.** The searchable text is short fields (street, city) and the incident record, not article
  bodies. `pg_trgm` is already in place with GIN indexes on street and city, which also powers duplicate
  matching. PostgreSQL full-text search (`tsvector`) can cover circumstances text if it is ever wanted.
  Article bodies are retained for 90 days and are not a search corpus.

## Revisit when
- A statistics query exceeds roughly a second on real data. Try a materialized view first, then Redis.
- The crawler runs as several replicas and per-domain politeness must hold across all of them. First use
  Temporal's per-queue rate limiting or a single replica; Redis only if that falls short.
- Users need ranked search over article text rather than filters on incident fields.

## Consequences
Fewer moving parts, fewer NetworkPolicies and secrets in the infra PR, and nothing extra to back up.
