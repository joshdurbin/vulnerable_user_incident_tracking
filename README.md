# Vulnerable Road User Collision Intelligence Platform

Finds news reports of motor-vehicle collisions involving vulnerable road users (pedestrians, cyclists, scooter and
wheelchair users, motorcyclists), extracts structured facts with an LLM, merges reports of the same collision into
one incident, and shows the result on a map with trends and a review queue.

Go owns the services, Python only crawls, a React app is the UI, and Temporal orchestrates everything in between.
Design: [`docs/design.md`](docs/design.md). Decisions: [`docs/decisions/`](docs/decisions). Plan and progress:
[`docs/implementation-plan.md`](docs/implementation-plan.md).

```mermaid
flowchart LR
  src[(GDELT, Google News,<br/>feeds, sitemaps)] --> disc
  subgraph temporal[Temporal workflows]
    disc[Discover<br/>on a Schedule per source] --> art[Article<br/>fetch, classify, resolve]
    art --> inc[Incident<br/>follow-ups, human review]
  end
  art -- fetch_article --> py[Python crawler<br/>SSRF-guarded]
  art -- classify --> llm[LLM via Bifrost<br/>OpenRouter]
  art --> pg[(PostgreSQL + PostGIS)]
  inc --> pg
  pg --> api[REST API<br/>OpenAPI] --> web[Web UI<br/>map, trends, reviews, ops]
  web -- "approve / reject" --> api -- "validated Update" --> inc
```

## Status
Built and tested: discovery providers, the Python crawler worker, LLM classification with an evaluation harness,
PostGIS persistence with entity resolution, the Temporal workflows (discovery, article, incident, backfill,
reprocess), the REST API, the web UI, a Helm chart with a Grafana dashboard and alerts, and runbooks
(`docs/runbooks/`). Not yet run on the cluster: the infra changes are an unmerged branch and the images are not built.
See "Not built yet" in the design for the full list.

## Quick start (no Kubernetes)
```bash
make test                           # Go + Python tests (no network)
# The Python crawler is a Temporal worker, so fetching needs a Temporal server and both workers:
temporal server start-dev --namespace default &
(cd crawler && CRAWLER_TEMPORAL_ADDRESS=localhost:7233 CRAWLER_TEMPORAL_NAMESPACE=default uv run collision-crawler) &
# Optional, for JavaScript-built pages: uv sync --extra browser && uv run playwright install chromium, then run a second
# worker with CRAWLER_MODE=render and start the others with COLLISION_CRAWLER_RENDER_FALLBACK=true.
export COLLISION_TEMPORAL_ADDRESS=localhost:7233 COLLISION_TEMPORAL_NAMESPACE=default
export COLLISION_DATABASE_URL=postgres://... COLLISION_LLM_MODEL=... COLLISION_LLM_BASE_URL=...
go run ./cmd/collisionctl migrate
go run ./cmd/collisionctl worker &                      # hosts the workflows and Go activities
go run ./cmd/collisionctl sources import --file docs/examples/sources.yaml   # optional starter set
go run ./cmd/collisionctl schedules sync
go run ./cmd/collisionctl api &                         # REST API on :8080
go run ./cmd/collisionctl fetch https://example.org/article --json
```
Backfills and reprocessing: `collisionctl backfill start|status|pause|resume|cancel`, `collisionctl reprocess start|status`.

## API
`collisionctl api` serves the REST API described in `api/openapi.yaml` (regenerate server code with
`make generate`). Reads are open (the service is tailnet-only); POST routes need
`Authorization: Bearer $COLLISION_API_ADMIN_TOKEN` and are disabled when no token is set.

## Web UI
`web/` is a React + Vite + TypeScript app (map, trends, incident detail, review queue, operations). Types come from
`api/openapi.yaml`. `cd web && npm run dev` serves on :5173 and proxies `/v1` to `collisionctl api` on :8080. The
container is nginx serving the build, proxying `/v1` to `$API_UPSTREAM`, and writing `config.json` (Temporal UI
link) from `TEMPORAL_UI_URL` at start. The map loads tiles from OpenFreeMap in the browser.

## Layout
```mermaid
flowchart LR
  cmd[cmd/collisionctl<br/>CLI, worker, api] --> internal
  subgraph internal[internal/]
    discovery --> pipeline
    classification --> pipeline
    incidents --> pipeline
    pipeline --> api
    crawler[crawler<br/>Go side] --> pipeline
    storage[storage + dbgen<br/>sqlc] --- incidents
    llm --> classification
  end
  py[crawler/<br/>Python worker] -. Temporal .- crawler
  db[db/migrations<br/>db/queries] --> storage
  spec[api/openapi.yaml] --> api
  spec --> web[web/<br/>React + Vite]
  deploy[deploy/<br/>Dockerfiles, compose]
```
`docs/` design, decisions and plan; `tests/evaluations/` labeled cases for the eval harness.

## Tests
```bash
make test               # Go + Python unit tests, no network or Docker needed
make test-integration   # PostGIS integration tests (needs Docker; uses the CNPG PostGIS image)
make eval-replay        # eval harness check only (author-written responses, not model quality)
```
Configuration is viper-based: flags > `COLLISION_*` env vars > `collisionctl.yaml` > defaults
(e.g. `COLLISION_LLM_MODEL`, `COLLISION_LLM_BASE_URL`, `COLLISION_DATABASE_URL`, `COLLISION_TEMPORAL_ADDRESS`).
