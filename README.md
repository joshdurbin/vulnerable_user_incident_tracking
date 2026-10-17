# Vulnerable Road User Collision Intelligence Platform

Go services plus a Python crawler that discover, classify and de-duplicate news reports of motor-vehicle
collisions involving vulnerable road users. See `starting_design.md` for the spec and
`docs/implementation-plan.md` for the plan and decisions.

## Status
Milestones 1-3 in progress: crawler + fetch, LLM classification + eval harness, PostGIS persistence + entity resolution. See docs/implementation-plan.md.

## Quick start (no Kubernetes)
```bash
make test                           # Go + Python tests (no network)
# The Python crawler is a Temporal worker, so fetching needs a Temporal server and both workers:
temporal server start-dev --namespace default &
(cd crawler && CRAWLER_TEMPORAL_ADDRESS=localhost:7233 CRAWLER_TEMPORAL_NAMESPACE=default uv run collision-crawler) &
go run ./cmd/collisionctl worker --temporal-namespace default ... &   # hosts the workflows
go run ./cmd/collisionctl fetch https://example.org/article --temporal-address localhost:7233 --temporal-namespace default --json
```

## Layout
`crawler/` Python Temporal worker (fetch + extraction), `internal/` Go packages,
`cmd/` binaries, `deploy/` Dockerfiles and compose.

## Tests
```bash
make test               # Go + Python unit tests, no network or Docker needed
make test-integration   # PostGIS integration tests (needs Docker; uses the CNPG PostGIS image)
make eval-replay        # eval harness check only (author-written responses, not model quality)
```
Configuration is viper-based: flags > `COLLISION_*` env vars > `collisionctl.yaml` > defaults
(e.g. `COLLISION_LLM_MODEL`, `COLLISION_LLM_BASE_URL`, `COLLISION_DATABASE_URL`, `COLLISION_TEMPORAL_ADDRESS`).
