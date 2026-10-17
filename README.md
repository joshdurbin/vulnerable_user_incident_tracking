# Vulnerable Road User Collision Intelligence Platform

Go services plus a Python crawler that discover, classify and de-duplicate news reports of motor-vehicle
collisions involving vulnerable road users. See `starting_design.md` for the spec and
`docs/implementation-plan.md` for the plan and decisions.

## Status
Milestones 1-3 in progress: crawler + fetch, LLM classification + eval harness, PostGIS persistence + entity resolution. See docs/implementation-plan.md.

## Quick start (no Kubernetes)
```bash
make generate                       # needs buf, protoc-gen-go, protoc-gen-go-grpc, uv
make test                           # Go + Python tests (no network)
cd crawler && uv run collision-crawler &          # listens on :9090
go run ./cmd/collisionctl fetch https://example.org/article --crawler localhost:9090 --json
```
Or `make dev-up` to run the crawler in Docker (requires a Docker daemon).

## Layout
`proto/` contracts, `gen/` generated Go, `crawler/` Python service, `internal/` Go packages,
`cmd/` binaries, `deploy/` Dockerfiles and compose.

## Tests
```bash
make test               # Go + Python unit tests, no network or Docker needed
make test-integration   # PostGIS integration tests (needs Docker; uses the CNPG PostGIS image)
make eval-replay        # eval harness check only (author-written responses, not model quality)
```
Configuration is viper-based: flags > `COLLISION_*` env vars > `collisionctl.yaml` > defaults
(e.g. `COLLISION_LLM_MODEL`, `COLLISION_LLM_BASE_URL`, `COLLISION_DATABASE_URL`, `COLLISION_CRAWLER_ADDR`).
