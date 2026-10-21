# Changing the model or the prompt

LLM spend is tracked outside this application, so nothing here reports cost. What this application bounds is volume:
the article limit on a backfill and on a reprocess run (Admin, Backfill & reprocess).

## Too many articles are being classified

1. Check for a backfill or reprocess running: Temporal UI, `CollisionKind = "backfill"` or `"reprocess"`. Cancel it
   in Admin, Backfill & reprocess. Both have article limits.
2. Check whether one source is flooding in non-relevant articles (Admin page: low "Relevant" rate with a high URL
   count). Disable it or tighten its queries.
3. Check for repeated classification. It should not happen (a stored extraction is reused for the same model, prompt and
   schema); `select article_id, count(*) from article_extractions group by 1 having count(*) > 2` shows articles with many
   versions.

## Changing the model or the prompt

A new model, prompt version or schema version creates new extractions beside the old ones. Nothing is overwritten until
you reprocess.

1. There is no evaluation command (the one that scored 15 author-written cases was removed, see the backlog). Judge a new
   model on real articles: step 3 with a small limit shows how many extractions change, and the Admin page and the review
   queue show what the new model does to real incidents.
2. Roll out the new model for new articles only (change `llm.model`, sync).
3. Reprocess history in a bounded run: Admin, Backfill & reprocess, "Start a reprocess run" with a limit of 100. Each article is re-classified, compared
   with its previous extraction, the old contribution is withdrawn, and the new one is resolved. Check the result in the same
   table (examined, changed, failed) before running a larger batch.
4. Incidents that lose all supporting articles are withdrawn (hidden, kept for audit), not deleted.
