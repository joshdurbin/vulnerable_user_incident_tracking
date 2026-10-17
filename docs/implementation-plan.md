# Implementation plan

Companion to `docs/design.md` (the current design). Records the cluster findings, the decisions made with the owner, and the ordered steps.

## Decisions (confirmed)

| Topic | Decision |
|---|---|
| PostGIS | Switch the shared CNPG cluster image to the CNPG PostGIS variant (Debian trixie, PG 18). Rolling restart; brief failover for Temporal/Bifrost/Grafana. Verify the exact image tag and digest before the PR. |
| LLM | Bifrost (`bifrost.bifrost:8080/v1`) with an OpenRouter provider (key entered in the Bifrost UI). Local Qwen2.5-1.5B is too slow for extraction. |
| Images | GitHub Actions, multi-arch buildx, push to ghcr.io. Pulls go through the Zot cache. Pin by digest. |
| Exposure | Tailnet only (Tailscale Ingress). Admin endpoints still require a token. |
| Frontend | React + Vite + TypeScript, MapLibre, a charting lib. Own container (nginx). |
| Python crawler | httpx + trafilatura + lxml; Playwright optional, off by default. No Scrapy unless an ADR shows it is needed. |

## Cluster facts that constrain the design

- Temporal: `temporal-frontend.temporal:7233`, SQL visibility, 7d default retention. Create a `collisions` namespace.
- Postgres: connect only via `postgres-pooler-rw.postgres:5432`; shared pods limited to 1Gi. Roles/DBs declared with CNPG `DatabaseRole`/`Database`.
- Redis and OpenSearch: not used (see `docs/decisions/0003-no-redis-no-opensearch.md`).
- Default-deny egress NetworkPolicy in every namespace; ours needs explicit DNS, Postgres, Temporal, Bifrost and public-HTTPS (crawler only) rules.
- Secrets come from Ansible Vault in the infra repo, not git.
- Pi 4B = 4GB, Pi 5 = 8GB; nodes are 31-71% used. Pi 5 uses a 16K-page kernel (jemalloc broke ProxySQL). Chromium on Pi 5 is unverified.
- Infra changes land as a PR to `joshdurbin/home-pi-infrastructure` (AppProject destination, DB, secrets, NetworkPolicies, Argo Applications).

## Temporal showcase

1. Schedules per discovery provider (overlap SKIP, catch-up window, pause from UI).
2. `DiscoveryWorkflow` fans out `ArticleWorkflow` child workflows; workflow ID = hash(normalized URL) for free dedupe.
3. Long-lived `IncidentWorkflow` per incident: signals deliver articles serially (no resolution races), durable timer for follow-up checks (injury -> fatality).
4. Human-in-the-loop: possible matches wait on a validated Update (approve/reject/split); merge/split as a saga with compensation.
5. `BackfillWorkflow`: date windows, Continue-As-New, progress Query, pause/resume/cancel signals, heartbeats.
6. Task queues `crawl`, `llm`, `geocode` with separate concurrency and rate limits; per-domain politeness; LLM cost cap.
7. Custom search attributes (source, state, model version) surfaced in the frontend ops page; patching/versioning for prompt changes; replay tests in CI; tracing interceptor propagating correlation IDs into Python gRPC.

### How each feature is visible in the frontend

| Temporal feature | Frontend surface |
|---|---|
| Schedules | Sources page: pause/resume toggle per provider, next/last run |
| Backfill Query + Signals | Live progress bar with pause/resume/cancel buttons |
| Update with validator | Review queue: approve / reject / split a possible match |
| Search attributes | Ops page filters (failed articles by source, model version) |
| Workflow IDs | "View in Temporal" deep link on every incident and article |

### Notes
- Retried LLM activities reuse the persisted extraction, so a retry never pays for the model twice.
- Article bodies live in Postgres; workflow history carries IDs only.
- Per-activity retry policies: 429s back off; malformed JSON retries once on the fallback model.
- Acceptance test for Milestone 5: a chaos test kills workers mid-backfill and asserts no lost articles and no duplicate incidents.
- Limitation: the cluster's Temporal uses SQL visibility (no Elasticsearch), so custom search attributes work but advanced visibility queries are limited. Enough for the ops filters above.

## Steps

### Phase 0 - decisions and infra prerequisites
1. ADRs for the §18 investigations (GDELT limits, Google News URL resolution, Scrapy vs httpx, Trafilatura quality, Playwright ARM64, gRPC, workflow granularity, PostGIS resolution, structured-output support, cost accounting, recall measurement, retention/robots).
2. Infra PR: PostGIS image, `collisions` DB/role, Temporal namespace, AppProject namespace, NetworkPolicies, vault secrets.
3. Add OpenRouter to Bifrost; benchmark candidate models (Qwen3 family, gpt-oss) on a handful of fixtures.

### Phase 1 - foundation (Milestone 1)
4. Monorepo scaffold: Go module, `buf` protos, Python package, Makefile, docker-compose for local dev.
5. Python crawler (now a Temporal worker, see ADR 0002): SSRF/DNS-rebinding guards, redirect validation, size/time limits, robots.txt, per-domain limits, trafilatura, HTML fixtures + pytest.
6. Go-side crawler client and `collisionctl fetch`.
7. Multi-arch Dockerfiles; GitHub Actions workflow.

### Phase 2 - LLM and data model (Milestones 2-3)
8. Bifrost client with capability probing, JSON-validation fallback, token/cost accounting, versioned prompts; `collisionctl classify`.
9. Labeled eval set and harness (precision/recall, field accuracy, hallucination, cost per incident).
10. Migrations and `sqlc`; PostGIS candidate matching; deterministic resolver + LLM tie-break; audit trail and split.

### Phase 3 - discovery and durable pipeline (Milestones 4-5)
11. GDELT, Google News RSS, RSS/Atom/sitemap adapters; source registry; URL normalization; discovery provenance and quality metrics.
12. All workflows above; Temporal test-env tests; worker-kill chaos test showing no lost work and no duplicate incidents.

### Phase 4 - API and frontend
13. REST API (chi/stdlib), OpenAPI, pagination, filters, bbox, auth on admin routes.
14. Frontend pages: Map (clustered incidents + filters), Trends (time series, by user type, by region, heatmap), Incident detail (observations timeline, sources, Temporal deep link), Review queue, Sources/Ops (provider value, LLM spend, workflow status).

### Phase 5 - production (Milestone 6)
15. Helm chart (api, worker, scheduler, crawler, web): security contexts, probes, limits, `VMPodScrape`s, Tailscale Ingress.
16. Grafana dashboard ConfigMap; Argo Applications; first rollout limited to California with a short lookback.
17. Docs and runbooks.

## Top risks

| Risk | Mitigation |
|---|---|
| PostGIS image swap disrupts shared DB | Do it in a quiet window; `kubectl cnpg` status checks between instances; confirm Temporal reconnects. |
| Chromium on Pi 5 (16K pages) | Rendering off by default; test on a Pi 4B; pin crawler browser pods to a tested node class. |
| LLM cost/accuracy | Eval harness before scale-up; per-month budget enforced in the llm activity; fallback model. |
| Wrong merges | Deterministic rules first, review queue for ambiguity, merge audit + split. |
| Publisher terms / copyright | Respect robots.txt; store excerpts and short evidence, not full-text redistribution; retention policy ADR. |
| Memory pressure on small nodes | Small requests/limits, one worker deployment, low concurrency, Playwright limited. |
