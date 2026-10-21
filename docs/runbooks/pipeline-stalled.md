# The pipeline has stalled

Alerts: `CollisionNoNewIncidents`, `CollisionLLMWorkerNotPolling`, `CollisionCrawlerNotPolling`,
`CollisionRenderNotPolling` (only when rendering is on), `CollisionTaskQueueBacklog`, `CollisionActivitiesFailing`, `CollisionMostArticlesFailing`.

Work flows: Schedule, discovery, fetch (crawler), classify (LLM), resolve, incident. Find the first stage that has stopped.

1. **Are the pods up?** `kubectl -n collision-tracker get pods`. A crash loop in the worker is usually a bad
   `COLLISION_LLM_MODEL`, an unreachable database, or an unreachable Temporal. Read `kubectl logs`.
2. **Are workers polling?** Dashboard panel "Pollers per task queue". Zero on `collision_llm` means no Go worker;
   zero on `collision-crawl` means no Python worker. (The Go side sanitises label values, so its queue shows with
   underscores.) A worker that cannot reach Temporal logs connection errors.
3. **Is work waiting?** "Schedule-to-start latency" rising means a queue is saturated or has no worker. For the LLM queue,
   raise `workers.<name>.resources` or split it into its own worker entry; for the crawler, raise
   `crawler.maxConcurrentFetches` carefully (publishers rate limit).
4. **Are activities failing?** Dashboard "Activity failures per minute" names the activity.
   - `fetch_article` failing for everything: the network policy, DNS, or the crawler's egress. Check
     `kubectl -n collision-tracker exec deploy/collision-tracker-crawler -- python -c "import urllib.request as u; print(u.urlopen('https://example.com').status)"`.
   - `render_article` failing or timing out: the render pods are out of memory (`kubectl -n collision-tracker get pods`
     shows `OOMKilled`), or too many pages are open at once. Lower `render.browserConcurrency` or raise
     `render.resources.limits.memory`. Articles keep flowing meanwhile: they fall back to the plain HTTP result, and pages
     that needed a browser are stored with no text. They are not fetched again automatically: reprocessing re-classifies stored text, it does not re-fetch.
   - `Classify` failing: Bifrost or OpenRouter. Check Bifrost's UI for the provider key, quota and error log.
   - Database activities failing: PostgreSQL or the pooler.
5. **Articles failing en masse** (`CollisionMostArticlesFailing`): publishers are blocking or the crawler's network is
   broken. Look at `article_fetch_attempts.error_detail` for the common reason.
6. **Nothing is wrong but no incidents:** discovery may simply be finding nothing relevant. Check the sources' "relevant"
   rate on the Admin page and the discovery workflow results in the Temporal UI.
7. **Stuck workflows.** In the Temporal UI, filter `CollisionKind = "article"` and `ExecutionStatus = "Running"` and open the
   oldest. Its pending activity shows what it is waiting for. Safe to terminate: re-discovery re-lists any URL without an
   article, and stored extractions mean no model call is repeated.

## Recovering a backlog

After an outage, run discovery again with a window that covers it:
Admin, Backfill & reprocess, "Start a backfill": pick the source, the dates and an article limit (500 is a sensible
start). Duplicates are ignored by URL, so overlapping windows are safe. The table shows progress and offers pause,
resume and cancel.
