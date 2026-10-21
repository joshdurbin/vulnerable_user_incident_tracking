# 0006: Incident search in OpenSearch

Status: accepted. Supersedes the OpenSearch half of [0003](0003-no-redis-no-opensearch.md).

## Context
0003 said no to OpenSearch because nothing needed ranked text search: filters on incident fields were enough, and `pg_trgm`
covered duplicate matching. People now want to type "fatal cyclist Telegraph" and get the best matches across streets,
cities, categories, circumstances and the headlines of the articles behind each incident. The cluster already runs a shared
OpenSearch (it holds the logs and Temporal's visibility), so the cost is a client, an index and one more dependency to
configure, not a new service to run.

## Decision
- **PostgreSQL stays the system of record.** The index is derived from it and holds only what is searched. A search returns
  incident IDs; the API loads the records from PostgreSQL and applies every other filter there, so filter semantics have
  one definition and the index can never show a record the database does not have.
- **Duplicate matching stays in PostgreSQL.** Entity resolution runs in one transaction under a per-state advisory lock
  and must see the incident another worker created a moment ago. OpenSearch is eventually consistent, so it would let
  duplicates through. `pg_trgm` stays for that.
- **Indexing follows `updated_at`, not hooks.** Every change to an incident (new article, merge, split, review,
  withdrawal) already bumps `incidents.updated_at`. A Temporal Schedule runs the indexer every minute: it reads incidents
  after a saved `(updated_at, id)` watermark, writes them in bulk, and only then advances the watermark. Merged and
  withdrawn incidents are deleted from the index. Rows younger than ten seconds are held back so a transaction that has
  set `updated_at` but not committed cannot be skipped for good. A rebuild is "forget the watermark".
- **Article text is not indexed.** Bodies are kept for 90 days and then removed; an index of them would outlive that
  policy. Titles, publishers and descriptions are kept for as long as the article row is, and are indexed. Full-text over
  bodies is a separate decision.
- **Off unless configured.** With no `COLLISION_SEARCH_URL` the API answers `?q=` with 503 "search is not configured", no
  indexer runs and the schedule does not exist. The API starts even if the cluster is down.
- **A least-privilege user.** The app's OpenSearch user can only touch `collision-incidents*`; it is not the admin account.
- **Versioned index name** (`collision-incidents-v1`). A mapping change bumps the suffix and a rebuild fills the new index.

## Consequences
- A change is searchable about a minute after it is saved, not instantly.
- Search returns at most the 500 best matches; filters then narrow that list, so a very broad query with a narrow filter can
  miss results that rank below 500. Raising the cap is a constant.
- One more dependency in the API's and worker's network policies, and a password to provision (derived, no new Vault entry).
- The index needs no backup.
- Not yet measured: relevance on real articles. The ranking weights (street 3, city 2, labels 2, titles 2) are reasonable
  guesses, tested on synthetic incidents only.
