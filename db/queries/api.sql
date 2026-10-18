-- Read models for the REST API. Filters are optional (NULL = no filter) and shared by
-- the list and statistics queries. Merged and withdrawn incidents are never listed.

-- name: ListIncidentsAPI :many
SELECT v.*, count(*) OVER()::bigint AS total
FROM incidents_v v JOIN incidents i ON i.id = v.id
WHERE v.merged_into IS NULL AND v.verification_status <> 'withdrawn'
  AND (sqlc.narg(min_lon)::float8 IS NULL OR i.geom && ST_MakeEnvelope(sqlc.narg(min_lon)::float8, sqlc.narg(min_lat)::float8, sqlc.narg(max_lon)::float8, sqlc.narg(max_lat)::float8, 4326)::geography)
  AND (sqlc.narg(from_date)::date IS NULL OR v.occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR v.occurred_on <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(state)::text IS NULL OR v.state = sqlc.narg(state)::text)
  AND (sqlc.narg(city)::text IS NULL OR lower(v.city) = lower(sqlc.narg(city)::text))
  AND (sqlc.narg(vru)::text[] IS NULL OR v.vru_categories && sqlc.narg(vru)::text[])
  AND (sqlc.narg(severity)::text IS NULL OR v.severity = sqlc.narg(severity)::text)
  AND (sqlc.narg(verification)::text IS NULL OR v.verification_status = sqlc.narg(verification)::text)
  AND (sqlc.narg(hit_and_run)::text IS NULL OR v.hit_and_run = sqlc.narg(hit_and_run)::text)
ORDER BY v.occurred_on DESC NULLS LAST, v.id
LIMIT sqlc.arg(max_rows)::int OFFSET sqlc.arg(skip_rows)::int;

-- name: StatsSummaryAPI :one
SELECT count(*)::bigint AS incidents,
       COALESCE(sum(COALESCE(v.fatality_count, 0)), 0)::bigint AS fatalities,
       COALESCE(sum(COALESCE(v.injured_count, 0)), 0)::bigint AS injured,
       count(*) FILTER (WHERE v.severity = 'fatal')::bigint AS fatal_incidents,
       count(*) FILTER (WHERE v.hit_and_run = 'yes')::bigint AS hit_and_run,
       count(*) FILTER (WHERE v.lat IS NOT NULL)::bigint AS with_coordinates,
       count(*) FILTER (WHERE v.verification_status IN ('corroborated','reviewed'))::bigint AS corroborated,
       COALESCE(sum(v.article_count), 0)::bigint AS articles
FROM incidents_v v JOIN incidents i ON i.id = v.id
WHERE v.merged_into IS NULL AND v.verification_status <> 'withdrawn'
  AND (sqlc.narg(min_lon)::float8 IS NULL OR i.geom && ST_MakeEnvelope(sqlc.narg(min_lon)::float8, sqlc.narg(min_lat)::float8, sqlc.narg(max_lon)::float8, sqlc.narg(max_lat)::float8, 4326)::geography)
  AND (sqlc.narg(from_date)::date IS NULL OR v.occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR v.occurred_on <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(state)::text IS NULL OR v.state = sqlc.narg(state)::text)
  AND (sqlc.narg(city)::text IS NULL OR lower(v.city) = lower(sqlc.narg(city)::text))
  AND (sqlc.narg(vru)::text[] IS NULL OR v.vru_categories && sqlc.narg(vru)::text[])
  AND (sqlc.narg(severity)::text IS NULL OR v.severity = sqlc.narg(severity)::text)
  AND (sqlc.narg(verification)::text IS NULL OR v.verification_status = sqlc.narg(verification)::text)
  AND (sqlc.narg(hit_and_run)::text IS NULL OR v.hit_and_run = sqlc.narg(hit_and_run)::text);

-- name: StatsTimeseriesAPI :many
SELECT date_trunc(sqlc.arg(bucket)::text, v.occurred_on::timestamp)::date AS bucket,
       count(*)::bigint AS incidents,
       COALESCE(sum(COALESCE(v.fatality_count, 0)), 0)::bigint AS fatalities,
       COALESCE(sum(COALESCE(v.injured_count, 0)), 0)::bigint AS injured
FROM incidents_v v JOIN incidents i ON i.id = v.id
WHERE v.merged_into IS NULL AND v.verification_status <> 'withdrawn' AND v.occurred_on IS NOT NULL
  AND (sqlc.narg(min_lon)::float8 IS NULL OR i.geom && ST_MakeEnvelope(sqlc.narg(min_lon)::float8, sqlc.narg(min_lat)::float8, sqlc.narg(max_lon)::float8, sqlc.narg(max_lat)::float8, 4326)::geography)
  AND (sqlc.narg(from_date)::date IS NULL OR v.occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR v.occurred_on <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(state)::text IS NULL OR v.state = sqlc.narg(state)::text)
  AND (sqlc.narg(city)::text IS NULL OR lower(v.city) = lower(sqlc.narg(city)::text))
  AND (sqlc.narg(vru)::text[] IS NULL OR v.vru_categories && sqlc.narg(vru)::text[])
  AND (sqlc.narg(severity)::text IS NULL OR v.severity = sqlc.narg(severity)::text)
  AND (sqlc.narg(verification)::text IS NULL OR v.verification_status = sqlc.narg(verification)::text)
  AND (sqlc.narg(hit_and_run)::text IS NULL OR v.hit_and_run = sqlc.narg(hit_and_run)::text)
GROUP BY 1 ORDER BY 1;

-- name: StatsBreakdownAPI :many
-- dim is validated by the API: state | city | severity | hit_and_run | weekday | month | vru | vehicle.
-- Incidents with several user types count once under each.
SELECT COALESCE(g.key, 'unknown')::text AS key,
       count(*)::bigint AS incidents,
       COALESCE(sum(COALESCE(v.fatality_count, 0)), 0)::bigint AS fatalities,
       COALESCE(sum(COALESCE(v.injured_count, 0)), 0)::bigint AS injured
FROM incidents_v v JOIN incidents i ON i.id = v.id
CROSS JOIN LATERAL (
    SELECT unnest(CASE
        WHEN sqlc.arg(dim)::text = 'vru' THEN COALESCE(NULLIF(v.vru_categories, '{}'), ARRAY[NULL]::text[])
        WHEN sqlc.arg(dim)::text = 'vehicle' THEN COALESCE(NULLIF(v.vehicle_categories, '{}'), ARRAY[NULL]::text[])
        WHEN sqlc.arg(dim)::text = 'state' THEN ARRAY[v.state]
        WHEN sqlc.arg(dim)::text = 'city' THEN ARRAY[v.city]
        WHEN sqlc.arg(dim)::text = 'severity' THEN ARRAY[v.severity]
        WHEN sqlc.arg(dim)::text = 'hit_and_run' THEN ARRAY[v.hit_and_run]
        WHEN sqlc.arg(dim)::text = 'weekday' THEN ARRAY[to_char(v.occurred_on, 'ID')]
        WHEN sqlc.arg(dim)::text = 'month' THEN ARRAY[to_char(v.occurred_on, 'MM')]
        ELSE ARRAY[NULL]::text[] END) AS key
) g
WHERE v.merged_into IS NULL AND v.verification_status <> 'withdrawn'
  AND (sqlc.narg(min_lon)::float8 IS NULL OR i.geom && ST_MakeEnvelope(sqlc.narg(min_lon)::float8, sqlc.narg(min_lat)::float8, sqlc.narg(max_lon)::float8, sqlc.narg(max_lat)::float8, 4326)::geography)
  AND (sqlc.narg(from_date)::date IS NULL OR v.occurred_on >= sqlc.narg(from_date)::date)
  AND (sqlc.narg(to_date)::date IS NULL OR v.occurred_on <= sqlc.narg(to_date)::date)
  AND (sqlc.narg(state)::text IS NULL OR v.state = sqlc.narg(state)::text)
  AND (sqlc.narg(city)::text IS NULL OR lower(v.city) = lower(sqlc.narg(city)::text))
  AND (sqlc.narg(vru)::text[] IS NULL OR v.vru_categories && sqlc.narg(vru)::text[])
  AND (sqlc.narg(severity)::text IS NULL OR v.severity = sqlc.narg(severity)::text)
  AND (sqlc.narg(verification)::text IS NULL OR v.verification_status = sqlc.narg(verification)::text)
  AND (sqlc.narg(hit_and_run)::text IS NULL OR v.hit_and_run = sqlc.narg(hit_and_run)::text)
GROUP BY 1 ORDER BY incidents DESC, 1
LIMIT sqlc.arg(max_rows)::int;

-- name: ListIncidentArticlesAPI :many
SELECT ia.article_id, ia.match_status, ia.match_score, ia.matched_by, a.title, a.publisher, a.final_url, a.published_at, a.fetched_at
FROM incident_articles ia JOIN articles a ON a.id = ia.article_id
WHERE ia.incident_id = $1
ORDER BY a.published_at NULLS LAST, ia.created_at;

-- name: ListReviewsAPI :many
SELECT * FROM match_reviews
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
ORDER BY created_at DESC
LIMIT sqlc.arg(max_rows)::int OFFSET sqlc.arg(skip_rows)::int;

-- name: ListIncidentsByIDs :many
SELECT * FROM incidents_v WHERE id = ANY(@ids::uuid[]);

-- name: PipelineCounts :one
SELECT (SELECT count(*) FROM discovered_urls)::bigint AS discovered_urls,
       (SELECT count(*) FROM articles)::bigint AS articles,
       (SELECT count(*) FROM articles WHERE status = 'failed')::bigint AS failed_articles,
       (SELECT count(*) FROM article_extractions)::bigint AS extractions,
       (SELECT count(*) FROM match_reviews WHERE status = 'pending')::bigint AS pending_reviews;

-- name: GetReviewNoLock :one
SELECT * FROM match_reviews WHERE id = $1;

-- name: MetricsArticleStatus :many
SELECT status, count(*)::bigint AS n FROM articles GROUP BY status;

-- name: MetricsIncidentTotals :one
SELECT count(*) FILTER (WHERE merged_into IS NULL AND verification_status <> 'withdrawn')::bigint AS active,
       count(*) FILTER (WHERE verification_status = 'withdrawn')::bigint AS withdrawn,
       COALESCE(max(created_at), 'epoch'::timestamptz)::timestamptz AS newest_created_at
FROM incidents;

-- name: MetricsExtractionTotals :many
SELECT model, count(*)::bigint AS extractions FROM article_extractions GROUP BY model;

-- name: MetricsSourceHealth :many
SELECT slug, enabled, consecutive_failures, last_success_at, poll_interval_seconds FROM discovery_sources ORDER BY slug;
