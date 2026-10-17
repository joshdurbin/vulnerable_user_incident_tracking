-- +goose Up
CREATE TABLE articles (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    discovered_url_id uuid UNIQUE REFERENCES discovered_urls(id) ON DELETE SET NULL,
    requested_url     text NOT NULL,
    final_url         text NOT NULL,
    canonical_url     text,
    title             text,
    publisher         text,
    author            text,
    published_at      timestamptz,
    language          text,
    description       text,
    -- Retention: full text is kept only until text_expires_at, then nulled by a
    -- scheduled job; extractions keep short evidence quotes, not the article.
    body_text         text,
    text_expires_at   timestamptz,
    content_hash      text,                 -- sha256 of body_text, survives text expiry
    status            text NOT NULL DEFAULT 'fetched' CHECK (status IN ('fetched','no_content','failed','blocked')),
    fetched_at        timestamptz NOT NULL DEFAULT now(),
    created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX articles_canonical_idx ON articles (canonical_url);
CREATE INDEX articles_hash_idx ON articles (content_hash);
CREATE INDEX articles_published_idx ON articles (published_at);
CREATE INDEX articles_text_expiry_idx ON articles (text_expires_at) WHERE body_text IS NOT NULL;

CREATE TABLE article_fetch_attempts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    discovered_url_id uuid NOT NULL REFERENCES discovered_urls(id) ON DELETE CASCADE,
    article_id        uuid REFERENCES articles(id) ON DELETE SET NULL,
    attempted_at      timestamptz NOT NULL DEFAULT now(),
    http_status       integer,
    rendered          boolean NOT NULL DEFAULT false,
    extraction_status text NOT NULL,
    error_detail      text,
    duration_ms       integer,
    correlation_id    text
);
CREATE INDEX article_fetch_attempts_url_idx ON article_fetch_attempts (discovered_url_id, attempted_at DESC);

-- One extraction per (article, model, prompt, schema): reprocessing with the same
-- versions is a no-op, so retries never pay for the LLM twice.
CREATE TABLE article_extractions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    article_id     uuid NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    model          text NOT NULL,
    prompt_version text NOT NULL,
    schema_version text NOT NULL,
    structured_mode text,
    verdict        text NOT NULL CHECK (verdict IN ('confirmed','possible','nonqualifying','insufficient')),
    article_kind   text NOT NULL,
    result         jsonb NOT NULL,          -- the full validated ExtractionResult
    warnings       text[] NOT NULL DEFAULT '{}',
    input_tokens   bigint NOT NULL DEFAULT 0,
    output_tokens  bigint NOT NULL DEFAULT 0,
    cost_usd       numeric(12,6) NOT NULL DEFAULT 0,
    latency_ms     integer NOT NULL DEFAULT 0,
    used_fallback  boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (article_id, model, prompt_version, schema_version)
);
CREATE INDEX article_extractions_version_idx ON article_extractions (model, prompt_version);
CREATE INDEX article_extractions_verdict_idx ON article_extractions (verdict);

-- +goose Down
DROP TABLE article_extractions, article_fetch_attempts, articles;
