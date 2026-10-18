# ADR 0005: Sources are configuration in the database, described by their providers

Status: accepted. Extends [ADR 0001](0001-discovery-provider-abstraction.md).

## Context
Discovery already ran every source through one `Provider` interface and read sources from `discovery_sources`. But the
sources themselves were embedded in the binary as YAML and re-seeded on every deploy, and knowledge about each source
type was spread over several places: lists of type names in the seed validator (`urlTypes`, `crawlTypes`), a
`discovery.IsSearchType` function and a `CHECK` constraint on `source_type`. Adding a source meant editing a file and
deploying; adding a type meant touching all of those. Health was three columns on the source row, with no history, and
one stored setting (`rate_limit_per_minute`) was never read by any provider.

## Decision
- **Sources and queries are data.** They are created, edited, tested and deleted in the admin UI over an admin API. Nothing
  is embedded and nothing is seeded; `collisionctl sources import|export` move them as YAML, and the former examples live
  in `docs/examples/sources.yaml`. Existing rows are untouched by this change.
- **A provider describes its types.** `Provider.Describe(sourceType) Descriptor` says whether a type takes a URL, runs
  queries and which crawl limits it reads. Validation and the admin form are generated from descriptors. The registry,
  not the database, decides which types exist (the `CHECK` is dropped), so a new type is one provider plus one registry
  line.
- **Inputs stay typed.** The common fields (URL, coverage, interval, crawl policy) cover every current provider. A generic
  per-source options blob was considered and rejected until a provider needs one; `rate_limit_per_minute` is removed
  because nothing read it.
- **Outputs are recorded.** `discovery.Result` is unchanged; `Service.Run` now writes a `discovery_runs` row for every
  non-dry run (status, counts, requests, warnings, error) in the same transaction as the source's health fields. Health
  (`healthy`, `degraded`, `failing`, `stale`, `paused`, `waiting`, `disabled`) is derived from that state by one pure,
  tested function. History is trimmed after 90 days.
- **The API owns the schedule lifecycle.** Saving a source applies its Temporal Schedule. `enabled` drives the pause
  state only when it changes, so an operator's pause survives other edits. A Temporal failure never loses a save; it is
  reported on the saved source. Delete removes the schedule first. `Sync schedules` (and `collisionctl schedules sync`,
  which the deploy hook runs) reconciles everything and removes schedules of deleted sources.
- **Test goes through Temporal.** The API makes no outbound requests (its network policy allows only Postgres and
  Temporal), so "Test" starts a `SourceTestWorkflow` whose activity runs the provider as a dry run on the discovery worker.
  It uses at most two queries for search sources so it answers while someone waits, and fails fast when no worker polls.
- **Admin reads need the token.** Source configuration and health are not public, so every `/admin` route, reads included,
  requires the bearer token. `/ops/pipeline` counters stay public.
- **LLM cost is not tracked here.** Spend is accounted for outside this application, so prices, the per-process budget,
  token and cost columns and their metrics, alert and dashboard panels are removed.

## Consequences
- A fresh install collects nothing until someone adds sources. That is intended, and the empty states say how.
- The deploy hook can no longer fix a source; it only keeps schedules in step with what is configured.
- Deleting a source deletes its run history; the sightings it produced stay, unattributed, so their articles keep their
  provenance by provider but lose the source slug.
- A source edited in the UI is not reflected in `docs/examples`; use `sources export` for a copy of what is configured.
- Per-run metrics are in the database and the admin UI. They are not exported to Prometheus beyond the existing
  per-source failure count, last success and interval.
