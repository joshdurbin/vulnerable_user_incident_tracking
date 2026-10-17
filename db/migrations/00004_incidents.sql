-- +goose Up
CREATE TABLE incidents (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Canonical (current best) values; the history lives in incident_observations.
    occurred_on         date,                       -- NULL = unknown
    occurred_at         timestamptz,                -- NULL unless a time was reported
    occurred_precision  text NOT NULL DEFAULT 'unknown' CHECK (occurred_precision IN ('datetime','date','unknown')),
    timezone            text,
    city                text,
    county              text,
    state               text,
    country             text NOT NULL DEFAULT 'US',
    street_address      text,
    location_precision  text NOT NULL DEFAULT 'unknown' CHECK (location_precision IN ('exact_address','intersection','block','street','neighborhood','city','county','unknown')),
    -- NULL when only a city/county is known: no fabricated coordinates.
    geom                geography(Point, 4326),
    geocode_source      text,
    geocode_confidence  real,
    geocode_precision   text,
    vru_categories      text[] NOT NULL DEFAULT '{}',
    vehicle_categories  text[] NOT NULL DEFAULT '{}',
    injured_count       integer CHECK (injured_count >= 0),
    fatality_count      integer CHECK (fatality_count >= 0),
    severity            text NOT NULL DEFAULT 'unknown' CHECK (severity IN ('fatal','serious','minor','unknown')),
    hit_and_run         text NOT NULL DEFAULT 'unknown' CHECK (hit_and_run IN ('yes','no','unknown')),
    circumstances       text,
    facts_status        text NOT NULL DEFAULT 'unclear' CHECK (facts_status IN ('confirmed','alleged','unclear')),
    verification_status text NOT NULL DEFAULT 'unverified' CHECK (verification_status IN ('unverified','corroborated','reviewed')),
    article_count       integer NOT NULL DEFAULT 0,
    merged_into         uuid REFERENCES incidents(id),   -- soft-merge target; row kept for audit
    version             integer NOT NULL DEFAULT 1,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incidents_geom_idx ON incidents USING gist (geom);
CREATE INDEX incidents_date_idx ON incidents (occurred_on) WHERE merged_into IS NULL;
CREATE INDEX incidents_state_date_idx ON incidents (state, occurred_on) WHERE merged_into IS NULL;
CREATE INDEX incidents_street_trgm_idx ON incidents USING gin (street_address gin_trgm_ops);
CREATE INDEX incidents_city_trgm_idx ON incidents USING gin (city gin_trgm_ops);
CREATE INDEX incidents_vru_idx ON incidents USING gin (vru_categories);

CREATE TABLE incident_participants (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('vru','vehicle')),
    category    text NOT NULL,
    count       integer,
    outcome     text NOT NULL DEFAULT 'unknown' CHECK (outcome IN ('fatal','injured','uninjured','unknown'))
    -- Deliberately no names or ages: personal data we do not need.
);
CREATE INDEX incident_participants_incident_idx ON incident_participants (incident_id);

-- Many-to-many: an article may describe several incidents, an incident has many articles.
CREATE TABLE incident_articles (
    incident_id   uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    article_id    uuid NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    extraction_id uuid NOT NULL REFERENCES article_extractions(id) ON DELETE CASCADE,
    candidate_idx integer NOT NULL,                 -- position within the extraction's incidents
    match_status  text NOT NULL CHECK (match_status IN ('created','confirmed','reviewed')),
    match_score   real,
    matched_by    text NOT NULL,                    -- 'rules' | 'llm' | 'human'
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (incident_id, article_id, extraction_id, candidate_idx)
);
-- Idempotency: one resolution per (extraction, candidate).
CREATE UNIQUE INDEX incident_articles_candidate_uniq ON incident_articles (extraction_id, candidate_idx);
CREATE INDEX incident_articles_article_idx ON incident_articles (article_id);

-- Every article's view of an incident, preserved. Canonical fields are derived from these.
CREATE TABLE incident_observations (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id   uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    article_id    uuid NOT NULL REFERENCES articles(id) ON DELETE CASCADE,
    extraction_id uuid NOT NULL REFERENCES article_extractions(id) ON DELETE CASCADE,
    candidate_idx integer NOT NULL,
    observed_at   timestamptz NOT NULL,             -- article publication time (else fetch time)
    data          jsonb NOT NULL,                   -- the IncidentCandidate as extracted
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (extraction_id, candidate_idx)
);
CREATE INDEX incident_observations_incident_idx ON incident_observations (incident_id, observed_at);

-- Append-only audit trail: creation, field changes, merges, splits, reviews.
CREATE TABLE incident_events (
    id          bigserial PRIMARY KEY,
    incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,
    event_type  text NOT NULL CHECK (event_type IN ('created','updated','conflict','merged','split','review_approved','review_rejected','geocoded')),
    actor       text NOT NULL,                      -- 'system' | 'llm' | user id
    data        jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_events_incident_idx ON incident_events (incident_id, id);

-- Possible matches awaiting a human decision (surfaced in the frontend review queue).
CREATE TABLE match_reviews (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    extraction_id         uuid NOT NULL REFERENCES article_extractions(id) ON DELETE CASCADE,
    candidate_idx         integer NOT NULL,
    new_incident_id       uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,  -- provisional incident created for the candidate
    candidate_incident_id uuid NOT NULL REFERENCES incidents(id) ON DELETE CASCADE,  -- existing incident it may duplicate
    score                 real NOT NULL,
    reasons               jsonb NOT NULL DEFAULT '{}',
    status                text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    decided_by            text,
    decided_at            timestamptz,
    created_at            timestamptz NOT NULL DEFAULT now(),
    UNIQUE (extraction_id, candidate_idx, candidate_incident_id)
);
CREATE INDEX match_reviews_pending_idx ON match_reviews (created_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE match_reviews, incident_events, incident_observations, incident_articles, incident_participants, incidents;
