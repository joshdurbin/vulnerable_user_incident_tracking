-- name: InsertArticle :one
INSERT INTO articles (discovered_url_id, requested_url, final_url, canonical_url, title, publisher, author,
                      published_at, language, description, body_text, text_expires_at, content_hash, status, fetched_at,
                      canonical_key, simhash, duplicate_of, duplicate_kind)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
RETURNING *;

-- name: GetArticle :one
SELECT * FROM articles WHERE id = $1;

-- name: UpsertExtraction :one
-- Idempotent on (article, model, prompt, schema): a repeat returns the existing row.
INSERT INTO article_extractions (article_id, model, prompt_version, schema_version, structured_mode, verdict, article_kind,
                                 result, warnings, latency_ms, used_fallback)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (article_id, model, prompt_version, schema_version) DO UPDATE SET model = EXCLUDED.model
RETURNING *, (xmax = 0) AS inserted;

-- name: GetExtraction :one
SELECT * FROM article_extractions WHERE id = $1;

-- name: FindDuplicateArticle :one
-- The earliest stored article this one repeats: the same text, the same canonical URL, or (within the
-- window) a near-identical text. Exact matches win over near ones. root_id is the original, since a
-- duplicate points at the article it copies and never at another copy.
SELECT m.id, COALESCE(m.duplicate_of, m.id)::uuid AS root_id, m.kind
FROM (
    SELECT a.id, a.duplicate_of, a.fetched_at, 1 AS prio, 'content_hash'::text AS kind
      FROM articles a WHERE a.status = 'fetched' AND a.content_hash = sqlc.arg(content_hash)::text
    UNION ALL
    SELECT a.id, a.duplicate_of, a.fetched_at, 2, 'canonical_url'::text
      FROM articles a WHERE a.status = 'fetched' AND a.canonical_key = sqlc.arg(canonical_key)::text
    UNION ALL
    SELECT a.id, a.duplicate_of, a.fetched_at, 3, 'near_duplicate'::text
      FROM articles a
     WHERE a.status = 'fetched' AND a.simhash IS NOT NULL AND sqlc.narg(simhash)::bigint IS NOT NULL
       AND a.fetched_at > sqlc.arg(since)::timestamptz
       AND bit_count((a.simhash # sqlc.narg(simhash)::bigint)::bit(64)) <= sqlc.arg(max_distance)::int
) m
ORDER BY m.prio, m.fetched_at
LIMIT 1;

-- name: BackfillArticleFingerprint :exec
UPDATE articles SET canonical_key = $2, simhash = $3 WHERE id = $1;

-- name: ListArticlesWithoutFingerprint :many
SELECT id, final_url, canonical_url, body_text FROM articles
WHERE status = 'fetched' AND body_text IS NOT NULL AND canonical_key IS NULL AND id > $1
ORDER BY id LIMIT $2;
