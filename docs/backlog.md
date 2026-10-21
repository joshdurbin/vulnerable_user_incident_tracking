# Backlog

Ideas that are not scheduled. Each says why it matters and what the first step would be, so it can be picked up later without
rereading the discussion. Nothing here is built.

## From the comparison with other systems (2026-10-18)

Desk research against Mapping Police Violence, the Gun Violence Archive, ACLED, GDELT, Streetsblog's fatality trackers,
crawler frameworks, LLM extraction tools, entity-resolution libraries and news duplicate detection. The architecture held up;
the gaps are in how much the numbers can be trusted. Ordered by value.

1. **Finish counting independent sources.** Copies of one article now count once (`duplicate_of`, see "Duplicate articles" in
   the design). Still open: tune the SimHash threshold and minimum length on real reviewed examples instead of synthetic edits;
   treat two different outlets of one owner (a chain's local sites) as one source; close the race where two copies stored at the
   same moment are both originals; fingerprint-then-mark existing articles as duplicates of each other.
2. **Measure recall against an official source.** Mapping Police Violence and the Gun Violence Archive publish coverage
   estimates (about 92% and 81%); this project cannot say what fraction of real incidents it captures. First step: pick a region
   and period, load NHTSA FARS (or the state's crash data) for fatal pedestrian and cyclist crashes, and match against incidents.
   Then show counts as lower bounds with the measured coverage beside them. Official crash records could later be a source type
   of their own, as Streetsblog uses police notifications.
3. **Replace the 15 author-written eval cases with real labelled articles.** ACLED's quality rests on coder-reliability checks;
   the bundled cases cannot show whether a model or prompt change helps. A few hundred real, labelled articles would let
   `collisionctl eval` gate changes and give a fair test of any small extraction model.
4. **Review extractions, not only merges.** Humans currently see only possible duplicates. A random sample of confirmed
   extractions, plus low-confidence ones, would catch systematic errors the evidence-quote check cannot (a real quote supporting
   the wrong road-user category).
5. **Record where evidence sits in the text, and handle long articles.** Store the character offset of each evidence quote so
   the incident page can highlight it (LangExtract does this). Article text is truncated at 12,000 characters, so an incident late
   in a long roundup is lost: chunk and run more than one pass, or send those articles to review.
6. **Poll sources more cheaply.** Feeds and sitemaps are fetched in full on every poll, with no `ETag` or `If-Modified-Since`.
   Add conditional requests. A title pre-filter before fetching would also cut wasted fetches (GDELT's irrelevant matches are
   noted in ADR 0001).
7. **Calibrate match thresholds with review decisions.** Merges use hand-set thresholds (auto 0.80, review 0.55 in
   `internal/incidents/score.go`). Every approve or reject in the review queue is a labelled pair. Keep them as data and check the
   thresholds against them. Not recommended: adopting Splink or learned weights at this volume.

Considered and rejected for now: a general crawler framework (Nutch, StormCrawler; the source set is bounded), a Firecrawl or
Crawl4AI style service (trafilatura plus the render pool already does this), and a small fine-tuned extraction model such as
NuExtract (worth testing only once item 3 exists; published results are vendor-reported).

## Earlier ideas

- Data model and indexes, measured on a 500k-row dataset: missing foreign-key indexes, a `lower(city)` index, rewriting
  `FindCandidates` without the UNION, keyset pagination or a capped count on incident lists, merging `incident_articles` into
  `incident_observations`, dropping `incident_participants`, dropping `articles.requested_url`, and storing `canonical_url` as NULL
  when it equals the final URL.
- UI: show uncertainty (lower-bound counts, corroboration) on the map and trends pages.
- An extracted-text size cap (about 200 KB).
- Postgres backups for the application database, and pinned chart revisions and image digests in the infra repo.
- Separate database roles for migrations and for runtime.
- Per-run discovery metrics in Prometheus and the Grafana dashboard (only per-source failures, last success and interval are
  exported today).
- An audit trail of who edited sources in the admin UI (edits are authorised by the shared token only).
- A "re-fetch no-content pages with rendering" command; pages stored as `NO_CONTENT` before rendering was enabled are not retried.
- Show who did what in the admin UI. The shared token carries no identity, so source edits, job controls and incident splits are
  attributed only to a typed name (splits) or nobody (the rest). Per-user login would fix both this and the audit-trail item above.
