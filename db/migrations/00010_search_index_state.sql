-- +goose Up
-- Where the search indexer has got to. The index is derived from incidents, ordered by (updated_at, id);
-- this row is the last incident it has written. Deleting the row makes the next run reindex everything.
CREATE TABLE search_index_state (
    name       text PRIMARY KEY,
    updated_at timestamptz NOT NULL,
    last_id    uuid NOT NULL,
    indexed_at timestamptz NOT NULL DEFAULT now()
);
-- The indexer reads incidents in (updated_at, id) order.
CREATE INDEX incidents_updated_idx ON incidents (updated_at, id);

-- +goose Down
DROP INDEX incidents_updated_idx;
DROP TABLE search_index_state;
