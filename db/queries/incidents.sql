-- name: LockKey :exec
SELECT pg_advisory_xact_lock(hashtext(@key::text));

-- name: FindResolution :one
SELECT incident_id, match_status FROM incident_articles WHERE extraction_id = $1 AND candidate_idx = $2;

-- name: InsertIncident :one
INSERT INTO incidents (occurred_on, occurred_at, occurred_precision, timezone, city, county, state, country, street_address,
                       location_precision, geom, geocode_source, geocode_confidence, geocode_precision,
                       vru_categories, vehicle_categories, injured_count, fatality_count, severity, hit_and_run,
                       circumstances, facts_status, article_count)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
        CASE WHEN sqlc.narg(lon)::float8 IS NULL OR sqlc.narg(lat)::float8 IS NULL THEN NULL
             ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography END,
        $11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,1)
RETURNING id;

-- name: GetIncident :one
SELECT * FROM incidents_v WHERE id = $1;

-- name: GetIncidentForUpdate :one
SELECT id, version FROM incidents WHERE id = $1 FOR UPDATE;

-- name: UpdateIncidentCanonical :exec
UPDATE incidents SET
    occurred_on = $2, occurred_at = $3, occurred_precision = $4, timezone = $5,
    city = $6, county = $7, state = $8, street_address = $9, location_precision = $10,
    geom = CASE WHEN sqlc.narg(lon)::float8 IS NULL OR sqlc.narg(lat)::float8 IS NULL THEN geom
                ELSE ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography END,
    vru_categories = $11, vehicle_categories = $12, injured_count = $13, fatality_count = $14,
    severity = $15, hit_and_run = $16, circumstances = $17, facts_status = $18,
    verification_status = $19, article_count = $20,
    version = version + 1, updated_at = now()
WHERE id = $1;

-- name: InsertIncidentArticle :exec
INSERT INTO incident_articles (incident_id, article_id, extraction_id, candidate_idx, match_status, match_score, matched_by)
VALUES ($1,$2,$3,$4,$5,$6,$7);

-- name: InsertObservation :exec
INSERT INTO incident_observations (incident_id, article_id, extraction_id, candidate_idx, observed_at, data)
VALUES ($1,$2,$3,$4,$5,$6);

-- name: ListObservations :many
SELECT * FROM incident_observations WHERE incident_id = $1 ORDER BY observed_at, created_at;

-- name: InsertEvent :exec
INSERT INTO incident_events (incident_id, event_type, actor, data) VALUES ($1,$2,$3,$4);

-- name: ListEvents :many
SELECT * FROM incident_events WHERE incident_id = $1 ORDER BY id;

-- name: ListIncidentArticles :many
SELECT ia.*, a.title, a.publisher, a.final_url FROM incident_articles ia JOIN articles a ON a.id = ia.article_id
WHERE ia.incident_id = $1 ORDER BY ia.created_at;

-- name: FindCandidates :many
-- Plausible duplicates: same state (or unknown), nearby date, and either spatially close or textually similar place.
SELECT v.*,
       -- -1 when either side has no coordinates
       COALESCE(CASE WHEN sqlc.narg(lon)::float8 IS NULL OR sqlc.narg(lat)::float8 IS NULL OR i.geom IS NULL THEN NULL
            ELSE ST_Distance(i.geom, ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography)::float8
       END, -1.0)::float8 AS distance_m,
       similarity(coalesce(v.street_address,''), coalesce(sqlc.narg(street)::text,''))::float4 AS street_sim,
       similarity(coalesce(v.city,''), coalesce(sqlc.narg(city)::text,''))::float4 AS city_sim
FROM incidents_v v JOIN incidents i ON i.id = v.id
WHERE v.merged_into IS NULL
  AND (sqlc.narg(state)::text IS NULL OR v.state IS NULL OR v.state = sqlc.narg(state)::text)
  AND (sqlc.narg(on_date)::date IS NULL OR v.occurred_on IS NULL
       OR v.occurred_on BETWEEN sqlc.narg(on_date)::date - sqlc.arg(window_days)::int AND sqlc.narg(on_date)::date + sqlc.arg(window_days)::int)
  AND (
        (sqlc.narg(lon)::float8 IS NOT NULL AND sqlc.narg(lat)::float8 IS NOT NULL AND i.geom IS NOT NULL
          AND ST_DWithin(i.geom, ST_SetSRID(ST_MakePoint(sqlc.narg(lon)::float8, sqlc.narg(lat)::float8), 4326)::geography, sqlc.arg(radius_m)::float8))
     OR (sqlc.narg(city)::text IS NOT NULL AND lower(v.city) = lower(sqlc.narg(city)::text))
     OR (sqlc.narg(county)::text IS NOT NULL AND lower(v.county) = lower(sqlc.narg(county)::text))
      )
ORDER BY v.occurred_on DESC NULLS LAST
LIMIT 50;

-- name: InsertMatchReview :exec
INSERT INTO match_reviews (extraction_id, candidate_idx, new_incident_id, candidate_incident_id, score, reasons)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (extraction_id, candidate_idx, candidate_incident_id) DO NOTHING;

-- name: GetReview :one
SELECT * FROM match_reviews WHERE id = $1 FOR UPDATE;

-- name: ListPendingReviews :many
SELECT * FROM match_reviews WHERE status = 'pending' ORDER BY created_at LIMIT $1;

-- name: DecideReview :exec
UPDATE match_reviews SET status = $2, decided_by = $3, decided_at = now() WHERE id = $1;

-- name: MoveIncidentArticles :exec
UPDATE incident_articles SET incident_id = @to_id WHERE incident_id = @from_id;

-- name: MoveObservations :exec
UPDATE incident_observations SET incident_id = @to_id WHERE incident_id = @from_id;

-- name: SoftMergeIncident :exec
UPDATE incidents SET merged_into = @to_id, article_count = 0, version = version + 1, updated_at = now() WHERE id = @from_id;

-- name: DeleteParticipants :exec
DELETE FROM incident_participants WHERE incident_id = $1;

-- name: InsertParticipant :exec
INSERT INTO incident_participants (incident_id, kind, category, count, outcome) VALUES ($1,$2,$3,$4,$5);

-- name: ListParticipants :many
SELECT * FROM incident_participants WHERE incident_id = $1 ORDER BY kind, category;

-- name: MoveArticleLinks :exec
UPDATE incident_articles SET incident_id = @to_id WHERE incident_id = @from_id AND article_id = @article_id;

-- name: MoveArticleObservations :exec
UPDATE incident_observations SET incident_id = @to_id WHERE incident_id = @from_id AND article_id = @article_id;

-- name: CountIncidentArticles :one
SELECT count(DISTINCT article_id)::int AS n FROM incident_articles WHERE incident_id = $1;

-- name: ListObservationsForArticle :many
SELECT * FROM incident_observations WHERE incident_id = $1 AND article_id = $2 ORDER BY candidate_idx;

-- name: SetGeocode :exec
UPDATE incidents SET geom = ST_SetSRID(ST_MakePoint(@lon::float8, @lat::float8), 4326)::geography,
    geocode_source = @source, geocode_confidence = @confidence, geocode_precision = @precision, updated_at = now()
WHERE id = @id;

-- name: SupersedeOtherReviews :exec
UPDATE match_reviews SET status = 'rejected', decided_by = 'system:superseded', decided_at = now()
WHERE new_incident_id = @new_incident_id AND status = 'pending' AND id <> @keep_id;
