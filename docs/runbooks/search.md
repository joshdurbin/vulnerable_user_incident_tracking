# Search is empty, stale or failing

Search is an index derived from PostgreSQL ([ADR 0006](../decisions/0006-search-in-opensearch.md)); nothing is lost when
it is wrong, only unfindable until it is rebuilt.

Start at Admin, Overview, "Search index". It shows how many incidents are indexed, how many are waiting, and when the indexer
last wrote.

| What you see | Meaning | What to do |
|---|---|---|
| "Search is not configured" | `COLLISION_SEARCH_URL` is not set on the API. | Set `search.enabled: true` in the chart values and sync. |
| A red "OpenSearch cannot be reached" | The API cannot talk to the cluster. Searches answer "search is not available right now". | Check the `opensearch` namespace is healthy, then the API's NetworkPolicy (egress to `opensearch:9200`), then the credentials (below). The map and everything else keep working. |
| "Waiting" stays above 0 and "Last written" is old | The indexer is not running or is failing. | Temporal UI: Schedules, `maintenance-search-index`, and its latest workflow (`search-index`). A paused schedule was paused on purpose. A failing run shows the OpenSearch error. |
| "Incidents indexed" is far below the number of incidents | The index was lost or created fresh. | "Rebuild index". It fills over a few minutes (4000 incidents per run, once a minute). |
| A new incident is not found | It is indexed about a minute after it is saved. | Wait; check "Waiting". |
| An old incident ranks oddly | The ranking is a set of guessed weights (`internal/search/doc.go`, `searchFields`). | Note the query and the incident; the weights are meant to be tuned on real examples. |

## Credentials and access
The API and worker log in as the OpenSearch user `collision`, which can only touch `collision-incidents*`. Its password is
derived from `collisions_db_password`; `make deploy-secrets` in the infra repo writes it to `opensearch-collision-credentials`
(opensearch namespace) and `collision-opensearch` (collision-tracker namespace). A 401 or 403 in the API log means one of those is
missing or the OpenSearch operator has not created the user yet (`kubectl -n opensearch get opensearchuser collision`).

## After a mapping change
The index name carries a version (`collision-incidents-v1`). A release that changes the mapping uses a new name; the first indexer
run creates it and fills it (reset the watermark with "Rebuild index" if the new index looks short). The old index is left behind;
delete it in OpenSearch Dashboards.

## Turning search off
`search.enabled: false` and sync. The next "Sync schedules" (or deploy) removes the schedule. The index in OpenSearch stays until
you delete it.
