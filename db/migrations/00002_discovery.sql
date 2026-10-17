-- +goose Up
CREATE TABLE discovery_sources (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                  text NOT NULL UNIQUE,
    name                  text NOT NULL,
    source_type           text NOT NULL CHECK (source_type IN ('gdelt','googlenews','rss','atom','sitemap','news_sitemap','index_page','police_press')),
    url                   text,
    coverage              jsonb NOT NULL DEFAULT '{}',   -- e.g. {"states":["CA"],"cities":["Oakland"]}
    poll_interval_seconds integer NOT NULL DEFAULT 1800 CHECK (poll_interval_seconds >= 60),
    enabled               boolean NOT NULL DEFAULT true,
    crawl_policy          jsonb NOT NULL DEFAULT '{}',
    rate_limit_per_minute integer NOT NULL DEFAULT 30,
    last_success_at       timestamptz,
    last_attempt_at       timestamptz,
    consecutive_failures  integer NOT NULL DEFAULT 0,
    error_state           text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovery_queries (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    family     text NOT NULL,               -- pedestrian, cyclist, scooter, motorcyclist, hit_and_run, ...
    name       text NOT NULL UNIQUE,
    query_text text NOT NULL,
    providers  text[] NOT NULL DEFAULT '{}', -- empty = all
    region     text,
    enabled    boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovered_urls (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    normalized_url text NOT NULL UNIQUE,
    domain         text NOT NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX discovered_urls_domain_idx ON discovered_urls (domain);

-- One row per (url, provider, source, query): an article can be found by several providers.
CREATE TABLE article_discoveries (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    discovered_url_id uuid NOT NULL REFERENCES discovered_urls(id) ON DELETE CASCADE,
    provider          text NOT NULL,
    source_id         uuid REFERENCES discovery_sources(id) ON DELETE SET NULL,
    query_id          uuid REFERENCES discovery_queries(id) ON DELETE SET NULL,
    discovered_at     timestamptz NOT NULL DEFAULT now(),
    published_at      timestamptz,           -- as reported by the provider
    raw_title         text,
    raw_url           text NOT NULL          -- URL exactly as the provider gave it
);
CREATE UNIQUE INDEX article_discoveries_uniq ON article_discoveries
    (discovered_url_id, provider, COALESCE(source_id, '00000000-0000-0000-0000-000000000000'), COALESCE(query_id, '00000000-0000-0000-0000-000000000000'));
CREATE INDEX article_discoveries_time_idx ON article_discoveries (discovered_at);

-- +goose Down
DROP TABLE article_discoveries, discovered_urls, discovery_queries, discovery_sources;
