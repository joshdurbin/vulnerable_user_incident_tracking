-- +goose Up
-- Duplicate detection. An article keeps its own row (so its URL is never fetched twice) but points at the
-- earliest article it repeats, and corroboration counts one source per original, not per copy.
--   canonical_key  normalized canonical (else final) URL: AMP pages, redirects and mirrors of one article
--   simhash        64-bit SimHash of the text's word shingles: wire copies and light rewrites
--   duplicate_of   the original article (never itself a duplicate), kind says how it was found
ALTER TABLE articles
    ADD COLUMN canonical_key  text,
    ADD COLUMN simhash        bigint,
    ADD COLUMN duplicate_of   uuid REFERENCES articles(id) ON DELETE SET NULL,
    ADD COLUMN duplicate_kind text CHECK (duplicate_kind IN ('content_hash','canonical_url','near_duplicate')),
    ADD CONSTRAINT articles_duplicate_pair CHECK ((duplicate_of IS NULL) = (duplicate_kind IS NULL)),
    ADD CONSTRAINT articles_not_own_duplicate CHECK (duplicate_of IS DISTINCT FROM id);
CREATE INDEX articles_canonical_key_idx ON articles (canonical_key) WHERE canonical_key IS NOT NULL;
CREATE INDEX articles_duplicate_of_idx ON articles (duplicate_of) WHERE duplicate_of IS NOT NULL;
-- The near-duplicate scan reads only recent articles that have a fingerprint.
CREATE INDEX articles_simhash_recent_idx ON articles (fetched_at) WHERE simhash IS NOT NULL;

-- +goose Down
DROP INDEX articles_simhash_recent_idx, articles_duplicate_of_idx, articles_canonical_key_idx;
ALTER TABLE articles
    DROP CONSTRAINT articles_not_own_duplicate, DROP CONSTRAINT articles_duplicate_pair,
    DROP COLUMN duplicate_kind, DROP COLUMN duplicate_of, DROP COLUMN simhash, DROP COLUMN canonical_key;
