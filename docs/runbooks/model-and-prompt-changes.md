# Changing the model or the prompt

LLM spend is tracked outside this application, so nothing here reports cost. What this application bounds is volume:
`--max-articles` on a backfill and `--limit` on a reprocess.

## Too many articles are being classified

1. Check for a backfill or reprocess running: Temporal UI, `CollisionKind = "backfill"` or `"reprocess"`. Cancel with
   `collisionctl backfill cancel <id>`. Both have article caps (`--max-articles`, `--limit`).
2. Check whether one source is flooding in non-relevant articles (Admin page: low "Relevant" rate with a high URL
   count). Disable it or tighten its queries.
3. Check for repeated classification. It should not happen (a stored extraction is reused for the same model, prompt and
   schema); `select article_id, count(*) from article_extractions group by 1 having count(*) > 2` shows articles with many
   versions.

## Changing the model or the prompt

A new model, prompt version or schema version creates new extractions beside the old ones. Nothing is overwritten until
you reprocess.

1. Evaluate first: `collisionctl eval` against the labelled cases (`tests/evaluations/cases`). The bundled cases are
   author-written; add real, labelled articles before trusting the scores.
2. Roll out the new model for new articles only (change `llm.model`, sync).
3. Reprocess history in a bounded run: `collisionctl reprocess start --limit 100`. Each article is re-classified, compared
   with its previous extraction, the old contribution is withdrawn, and the new one is resolved. Check the result with
   `collisionctl reprocess status <id>` (examined, changed, failed) before running a larger batch.
4. Incidents that lose all supporting articles are withdrawn (hidden, kept for audit), not deleted.
