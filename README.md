# Vulnerable Road User Collision Intelligence Platform

Go services plus a Python crawler that discover, classify and de-duplicate news reports of motor-vehicle
collisions involving vulnerable road users. See `starting_design.md` for the spec and
`docs/implementation-plan.md` for the plan and decisions.

## Status
Milestone 1 (fetch vertical slice): Python gRPC crawler, Go client, `collisionctl fetch`.

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
