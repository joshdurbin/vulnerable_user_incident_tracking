-- LLM spend is tracked outside this application. Token counts only existed to compute it.

-- +goose Up
ALTER TABLE article_extractions
    DROP COLUMN input_tokens,
    DROP COLUMN output_tokens,
    DROP COLUMN cost_usd;

-- +goose Down
ALTER TABLE article_extractions
    ADD COLUMN input_tokens  bigint NOT NULL DEFAULT 0,
    ADD COLUMN output_tokens bigint NOT NULL DEFAULT 0,
    ADD COLUMN cost_usd      numeric(12,6) NOT NULL DEFAULT 0;
