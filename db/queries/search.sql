-- name: GetSearchWatermark :one
SELECT updated_at, last_id, indexed_at FROM search_index_state WHERE name = $1;

-- name: SetSearchWatermark :exec
INSERT INTO search_index_state (name, updated_at, last_id, indexed_at) VALUES ($1, $2, $3, now())
ON CONFLICT (name) DO UPDATE SET updated_at = EXCLUDED.updated_at, last_id = EXCLUDED.last_id, indexed_at = now();

-- name: ResetSearchWatermark :exec
DELETE FROM search_index_state WHERE name = $1;

-- name: ListIncidentsToIndex :many
-- The next incidents after the watermark, with the metadata of their articles. Rows newer than
-- lag_seconds are held back: a transaction that set updated_at but has not committed yet could
-- otherwise be skipped for good once the watermark moves past it.
SELECT v.id, v.occurred_on, v.city, v.county, v.state, v.street_address, v.lat, v.lon,
       v.vru_categories, v.vehicle_categories, v.injured_count, v.fatality_count, v.severity, v.hit_and_run,
       v.circumstances, v.verification_status, v.article_count, v.merged_into, v.updated_at,
       COALESCE(a.titles, '{}')::text[] AS titles,
       COALESCE(a.publishers, '{}')::text[] AS publishers,
       COALESCE(a.descriptions, '{}')::text[] AS descriptions
FROM incidents_v v
LEFT JOIN LATERAL (
    SELECT array_agg(DISTINCT ar.title) FILTER (WHERE ar.title IS NOT NULL AND ar.title <> '') AS titles,
           array_agg(DISTINCT ar.publisher) FILTER (WHERE ar.publisher IS NOT NULL AND ar.publisher <> '') AS publishers,
           array_agg(DISTINCT ar.description) FILTER (WHERE ar.description IS NOT NULL AND ar.description <> '') AS descriptions
    FROM incident_articles ia JOIN articles ar ON ar.id = ia.article_id
    WHERE ia.incident_id = v.id
) a ON true
WHERE (v.updated_at, v.id) > (@after_updated_at::timestamptz, @after_id::uuid)
  AND v.updated_at <= now() - make_interval(secs => @lag_seconds::float8)
ORDER BY v.updated_at, v.id
LIMIT @max_rows::int;

-- name: CountIncidentsToIndex :one
SELECT count(*)::bigint FROM incidents v WHERE (v.updated_at, v.id) > (@after_updated_at::timestamptz, @after_id::uuid);
