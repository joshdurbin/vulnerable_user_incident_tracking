-- name: GetDiscoveredURL :one
SELECT * FROM discovered_urls WHERE id = $1;

-- name: GetArticleByDiscoveredURL :one
SELECT * FROM articles WHERE discovered_url_id = $1;

-- name: InsertFetchAttempt :exec
INSERT INTO article_fetch_attempts (discovered_url_id, article_id, http_status, rendered, extraction_status, error_detail, duration_ms, correlation_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: GetExtractionByVersion :one
SELECT * FROM article_extractions
WHERE article_id = $1 AND model = $2 AND prompt_version = $3 AND schema_version = $4;

-- name: LatestExtraction :one
SELECT * FROM article_extractions WHERE article_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1;

-- name: ListArticlesToReprocess :many
-- Articles with retained text that have no extraction for the current model/prompt/schema.
SELECT a.id FROM articles a
WHERE a.status = 'fetched' AND a.body_text IS NOT NULL AND a.id > @after::uuid
  AND NOT EXISTS (SELECT 1 FROM article_extractions x WHERE x.article_id = a.id
                  AND x.model = @model AND x.prompt_version = @prompt_version AND x.schema_version = @schema_version)
ORDER BY a.id
LIMIT @max_rows;

-- name: ListIncidentsByExtraction :many
SELECT DISTINCT incident_id FROM incident_articles WHERE extraction_id = $1;

-- name: DeleteLinksByExtraction :exec
DELETE FROM incident_articles WHERE extraction_id = $1;

-- name: DeleteObservationsByExtraction :exec
DELETE FROM incident_observations WHERE extraction_id = $1;

-- name: WithdrawIncident :exec
UPDATE incidents SET verification_status = 'withdrawn', article_count = 0, version = version + 1, updated_at = now() WHERE id = $1;

-- name: RejectReviewsForIncident :exec
UPDATE match_reviews SET status = 'rejected', decided_by = 'system:withdrawn', decided_at = now()
WHERE status = 'pending' AND (new_incident_id = $1 OR candidate_incident_id = $1);

-- name: ListPendingReviewsForIncident :many
SELECT * FROM match_reviews WHERE status = 'pending' AND new_incident_id = $1 ORDER BY created_at;

-- name: CountArticlesByStatus :one
SELECT count(*)::bigint FROM articles WHERE status = $1;

-- name: ListURLsWithoutArticle :many
SELECT u.id, u.normalized_url, u.domain FROM discovered_urls u
WHERE u.id = ANY(@ids::uuid[]) AND NOT EXISTS (SELECT 1 FROM articles a WHERE a.discovered_url_id = u.id);

-- name: ExpireArticleText :execrows
-- Retention: drop article bodies past their expiry. The content hash, metadata and the
-- extractions (with their short evidence quotes) are kept.
UPDATE articles SET body_text = NULL WHERE body_text IS NOT NULL AND text_expires_at IS NOT NULL AND text_expires_at < now();
