-- Sources are configured in the database and through the admin API, so the set of
-- source types is owned by the registered providers, not by a CHECK constraint, and
-- every run is recorded so a source's health can be seen and trended.
-- rate_limit_per_minute was stored but no provider ever read it.

-- +goose Up
ALTER TABLE discovery_sources DROP CONSTRAINT discovery_sources_source_type_check;
ALTER TABLE discovery_sources DROP COLUMN rate_limit_per_minute;

CREATE TABLE discovery_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id     uuid NOT NULL REFERENCES discovery_sources(id) ON DELETE CASCADE,
    started_at    timestamptz NOT NULL,
    finished_at   timestamptz NOT NULL,
    window_since  timestamptz,
    window_until  timestamptz,
    status        text NOT NULL CHECK (status IN ('success','partial','failed')),
    found         integer NOT NULL DEFAULT 0,   -- candidates the provider returned
    unique_urls   integer NOT NULL DEFAULT 0,   -- after normalization and dedupe within the run
    new_urls      integer NOT NULL DEFAULT 0,   -- never seen before
    known_urls    integer NOT NULL DEFAULT 0,
    pending_urls  integer NOT NULL DEFAULT 0,   -- seen before but never turned into an article
    unresolved    integer NOT NULL DEFAULT 0,
    requests      integer NOT NULL DEFAULT 0,   -- upstream requests made
    truncated     boolean NOT NULL DEFAULT false,
    warnings      text[] NOT NULL DEFAULT '{}',
    error         text
);
CREATE INDEX discovery_runs_source_idx ON discovery_runs (source_id, started_at DESC);
CREATE INDEX discovery_runs_started_idx ON discovery_runs (started_at);

-- +goose Down
DROP TABLE discovery_runs;
ALTER TABLE discovery_sources ADD COLUMN rate_limit_per_minute integer NOT NULL DEFAULT 30;
ALTER TABLE discovery_sources ADD CONSTRAINT discovery_sources_source_type_check
    CHECK (source_type IN ('gdelt','googlenews','rss','atom','sitemap','news_sitemap','index_page','police_press'));
