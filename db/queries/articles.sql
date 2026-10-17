-- name: InsertArticle :one
INSERT INTO articles (discovered_url_id, requested_url, final_url, canonical_url, title, publisher, author,
                      published_at, language, description, body_text, text_expires_at, content_hash, status, fetched_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
RETURNING *;

-- name: GetArticle :one
SELECT * FROM articles WHERE id = $1;

-- name: UpsertExtraction :one
-- Idempotent on (article, model, prompt, schema): a repeat returns the existing row.
INSERT INTO article_extractions (article_id, model, prompt_version, schema_version, structured_mode, verdict, article_kind,
                                 result, warnings, input_tokens, output_tokens, cost_usd, latency_ms, used_fallback)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (article_id, model, prompt_version, schema_version) DO UPDATE SET model = EXCLUDED.model
RETURNING *, (xmax = 0) AS inserted;

-- name: GetExtraction :one
SELECT * FROM article_extractions WHERE id = $1;
