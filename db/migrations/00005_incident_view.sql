-- +goose Up
-- Read model with plain lat/lon columns (NULL when no precise coordinates exist).
CREATE VIEW incidents_v AS
SELECT i.id, i.occurred_on, i.occurred_at, i.occurred_precision, i.timezone, i.city, i.county, i.state, i.country,
       i.street_address, i.location_precision,
       ST_Y(i.geom::geometry)::float8 AS lat, ST_X(i.geom::geometry)::float8 AS lon,
       i.geocode_source, i.geocode_confidence, i.geocode_precision,
       i.vru_categories, i.vehicle_categories, i.injured_count, i.fatality_count, i.severity, i.hit_and_run,
       i.circumstances, i.facts_status, i.verification_status, i.article_count, i.merged_into, i.version,
       i.created_at, i.updated_at
FROM incidents i;

-- +goose Down
DROP VIEW incidents_v;
