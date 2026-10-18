# Incidents, reviews and corrections

## The review queue

Possible duplicates (match score between 0.55 and 0.80, or two near-equal automatic matches) wait on the Reviews page.
Merge when they are the same collision; keep separate when they are not. Either decision is recorded with your name in the
incident's history. Decisions go to the incident's Temporal workflow as an Update; if the workflow is not running, the
API starts it.

If a decision returns 409 the review was already decided. If it returns 503 Temporal is unreachable; read routes still
work, retry later.

## Something looks wrong with an incident

Open the incident page: sources, what each article reported, and the history (including logged conflicts). The record is
derived from those observations, so a wrong value usually means one article said it.

- **Wrong merge.** `collisionctl incidents split <incident-id> <article-id> --actor <you>` moves that article's
  observations to a new incident and recomputes both. The ids are on the incident page (sources list). There is no UI
  button for this yet.
- **Bad extraction.** Fix the prompt or model and reprocess (`model-and-prompt-changes.md`).
- **An article that should not count.** Withdrawing is done by reprocessing; there is no manual delete by design.

## Data retention

Article text is kept for 90 days (`articles.text_expires_at`) and then removed by a daily Temporal Schedule
(`maintenance-retention`); extractions keep short
evidence quotes, not the article. No names or ages of people are stored.
