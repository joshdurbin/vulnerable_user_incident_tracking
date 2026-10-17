-- name: UpsertSource :one
INSERT INTO discovery_sources (slug, name, source_type, url, coverage, poll_interval_seconds, enabled, crawl_policy, rate_limit_per_minute)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name, source_type = EXCLUDED.source_type, url = EXCLUDED.url,
    coverage = EXCLUDED.coverage, poll_interval_seconds = EXCLUDED.poll_interval_seconds, enabled = EXCLUDED.enabled,
    crawl_policy = EXCLUDED.crawl_policy, rate_limit_per_minute = EXCLUDED.rate_limit_per_minute, updated_at = now()
RETURNING *;

-- name: GetSourceBySlug :one
SELECT * FROM discovery_sources WHERE slug = $1;

-- name: ListSources :many
SELECT * FROM discovery_sources WHERE (NOT @enabled_only::bool OR enabled) ORDER BY slug;

-- name: MarkSourceSuccess :exec
UPDATE discovery_sources SET last_success_at = @at, last_attempt_at = @at, consecutive_failures = 0, error_state = NULL, updated_at = now()
WHERE slug = @slug;

-- name: MarkSourceFailure :exec
UPDATE discovery_sources SET last_attempt_at = @at, consecutive_failures = consecutive_failures + 1, error_state = @err, updated_at = now()
WHERE slug = @slug;

-- name: UpsertQuery :one
INSERT INTO discovery_queries (family, name, query_text, providers, region, enabled)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (name) DO UPDATE SET family = EXCLUDED.family, query_text = EXCLUDED.query_text, providers = EXCLUDED.providers,
    region = EXCLUDED.region, enabled = EXCLUDED.enabled
RETURNING *;

-- name: ListQueriesForProvider :many
SELECT * FROM discovery_queries
WHERE enabled AND (cardinality(providers) = 0 OR @provider::text = ANY(providers))
ORDER BY family, name;

-- name: UpsertDiscoveredURL :one
INSERT INTO discovered_urls (normalized_url, domain) VALUES ($1,$2)
ON CONFLICT (normalized_url) DO UPDATE SET normalized_url = EXCLUDED.normalized_url
RETURNING id, (xmax = 0) AS inserted;

-- name: InsertArticleDiscovery :execrows
INSERT INTO article_discoveries (discovered_url_id, provider, source_id, query_id, published_at, raw_title, raw_url)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT DO NOTHING;

-- name: GetQueryIDByName :one
SELECT id FROM discovery_queries WHERE name = $1;

-- name: DiscoveryStats :many
-- Per provider + source over a recent window (design 5.7): is each source actually valuable?
WITH d AS (
    SELECT ad.*, s.slug AS source_slug
    FROM article_discoveries ad LEFT JOIN discovery_sources s ON s.id = ad.source_id
    WHERE ad.discovered_at >= @since::timestamptz
),
url_providers AS (
    SELECT discovered_url_id, count(DISTINCT provider) AS n FROM article_discoveries GROUP BY discovered_url_id
)
SELECT d.provider::text AS provider, COALESCE(d.source_slug, '')::text AS source_slug,
       count(*)::bigint AS discoveries,
       count(DISTINCT d.discovered_url_id)::bigint AS unique_urls,
       count(DISTINCT d.discovered_url_id) FILTER (WHERE up.n = 1)::bigint AS exclusive_urls,
       COALESCE(avg(extract(epoch FROM (d.discovered_at - d.published_at))) FILTER (WHERE d.published_at IS NOT NULL AND d.discovered_at >= d.published_at), 0)::float8 AS avg_delay_seconds,
       count(DISTINCT a.id)::bigint AS articles_fetched,
       count(DISTINCT a.id) FILTER (WHERE a.status = 'fetched')::bigint AS fetch_ok,
       count(DISTINCT x.article_id) FILTER (WHERE x.verdict IN ('confirmed','possible'))::bigint AS relevant_articles,
       count(DISTINCT ia.incident_id)::bigint AS incidents
FROM d
JOIN url_providers up ON up.discovered_url_id = d.discovered_url_id
LEFT JOIN articles a ON a.discovered_url_id = d.discovered_url_id
LEFT JOIN article_extractions x ON x.article_id = a.id
LEFT JOIN incident_articles ia ON ia.article_id = a.id
GROUP BY d.provider, COALESCE(d.source_slug, '')
ORDER BY discoveries DESC;

-- name: InsertSourceIfMissing :execrows
-- For first-install bootstrap: never overwrites what an operator changed.
INSERT INTO discovery_sources (slug, name, source_type, url, coverage, poll_interval_seconds, enabled, crawl_policy, rate_limit_per_minute)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (slug) DO NOTHING;

-- name: InsertQueryIfMissing :execrows
INSERT INTO discovery_queries (family, name, query_text, providers, region, enabled)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (name) DO NOTHING;
