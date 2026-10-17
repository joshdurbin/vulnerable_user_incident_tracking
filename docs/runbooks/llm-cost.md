# LLM cost, model and prompt changes

## Where spend shows up

- Dashboard: "LLM spend per hour", "LLM tokens per hour". Operations page: "LLM spend per day".
- Alert `CollisionLLMSpendHigh` fires above `monitoring.alerts.llmSpendUSDPerDay`.
- Cost is only tracked for models that have a price in `llm.prices`. A model without one logs a warning and records zero.

## Spend is higher than expected

1. Check for a backfill or reprocess running: Temporal UI, `CollisionKind = "backfill"` or `"reprocess"`. Cancel with
   `collisionctl backfill cancel <id>`. Both have article caps (`--max-articles`, `--limit`).
2. Check whether one source is flooding in non-relevant articles (Operations page: low "Relevant" rate with high URL
   count). Disable it or tighten its queries.
3. Check for repeated classification. It should not happen (a stored extraction is reused for the same model, prompt and
   schema); `select article_id, count(*) from article_extractions group by 1 having count(*) > 2` shows articles with many
   versions.
4. Set a hard stop: `llm.budgetUSD`. It is per worker process and resets when the pod restarts, so treat it as a
   safety net, not accounting. Articles that hit it end as "budget_exceeded" (not failures) and can be reprocessed.

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
