# Project: Vulnerable Road User Collision Intelligence Platform

You are a principal software engineer and systems architect. Your task is to design and implement a production-quality, open-source system that continuously discovers, crawls, classifies, extracts, deduplicates, and stores reports of motor vehicle collisions involving vulnerable road users (VRUs).

Work autonomously through architecture, implementation, testing, documentation, and deployment configuration. Make pragmatic technical decisions, document tradeoffs, and favor maintainability, operational simplicity, and correctness.

Do not stop after producing a design document. Implement a working system, with tests and local development tooling. When external services or credentials are unavailable, implement their integrations behind testable interfaces and provide local mocks or fixtures.

## 1. Project objectives

Build a continuously operating collision intelligence system that identifies traffic collisions involving people who are not occupants of enclosed passenger vehicles.

Relevant road users include:

- Pedestrians
- Bicyclists and e-bike riders
- Electric scooter riders
- Motorcyclists and moped riders
- Wheelchair and mobility-device users
- Skateboarders and other micromobility users
- Other vulnerable road users

The system should identify incidents involving motor vehicles that resulted in injury or death.

Do not automatically classify single-bicycle falls, scooter falls, or collisions without motor vehicles as qualifying incidents. Make inclusion policies configurable so these cases can be supported later.

The system must:

1. Continuously discover relevant articles and official reports.
2. Crawl publisher websites and extract readable article content.
3. Classify articles using inexpensive cloud-hosted open-weight LLMs.
4. Extract structured incident data.
5. Identify multiple incidents mentioned in one article.
6. Identify multiple articles describing the same incident.
7. Track changes to incidents over time, including injuries later becoming fatalities.
8. Geocode locations and support spatial queries.
9. Preserve provenance and uncertainty for extracted facts.
10. Expose incident data through an HTTP API.
11. Support historical backfills and reprocessing.
12. Run efficiently on ARM64 Kubernetes nodes, particularly Raspberry Pi 4B and Pi 5 hardware.
13. Provide meaningful observability, metrics, and operational controls.

Start with United States coverage, prioritizing California during initial validation. Do not hardcode California-specific assumptions into the domain model.

The system should eventually support nationwide operation.

## 2. Technology requirements

### Primary language: Go

Go must own all application and business logic other than crawling and parsing web content.

Use Go for:

- Discovery scheduling and coordination
- News API and RSS discovery adapters
- Temporal workflows and activities
- Job lifecycle management
- LLM gateway integration
- Classification and structured extraction orchestration
- Schema validation
- Incident entity resolution
- Deduplication
- Geocoding coordination
- Database persistence
- REST API
- Administrative CLI
- Configuration
- Metrics and tracing
- Health checks

Use modern, idiomatic Go. Prefer the standard library when reasonable and avoid unnecessary frameworks.

Suggested libraries:

- `pgx` for PostgreSQL
- `sqlc` if useful for typed SQL
- `chi` or Go standard-library routing
- Official Temporal Go SDK
- OpenAI-compatible Go client
- Prometheus client libraries
- OpenTelemetry
- Protobuf and gRPC

### Python: exclusively for crawling and parsing

Python must be restricted to:

- Website crawling
- HTTP page retrieval
- HTML parsing
- Article text extraction
- JavaScript browser rendering
- Link extraction and bounded website exploration
- Webpage metadata extraction

Suggested libraries:

- Scrapy
- Trafilatura
- Playwright
- scrapy-playwright
- BeautifulSoup/lxml where useful

Python must not own:

- Business logic
- LLM classification
- Incident entity resolution
- Database persistence
- Temporal workflows
- Scheduling
- Application APIs unrelated to crawling

Implement Python as an independently deployable, preferably stateless, gRPC service.

Go must communicate with Python using versioned Protobuf contracts.

### Database

Use PostgreSQL with PostGIS.

PostgreSQL is the authoritative source of truth for:

- Discovery sources
- Discovered URLs
- Articles
- Extraction results
- Incidents
- Incident participants
- Incident-to-article relationships
- Processing state
- Source provenance
- Incident history
- Geospatial data

Use database migrations under version control.

Design for idempotency and concurrent processing.

### Workflow orchestration

Use Temporal for durable orchestration.

The system will run against an existing Temporal deployment in Kubernetes. Do not require deploying a dedicated production Temporal server.

Provide a Temporal development environment for local testing.

Use Temporal schedules for recurring discovery where appropriate.

Avoid storing large article bodies in Temporal workflow history.

### LLM inference

Use an OpenAI-compatible gateway, such as Bifrost or LiteLLM, to access inexpensive cloud-hosted open-weight models.

The application must not depend on one model provider.

Support configurable:

- Gateway endpoint
- API key
- Primary model
- Fallback model
- Request timeout
- Concurrency
- Retry policy
- Token budgets
- Monthly spending limits
- Prompt versions
- Model versions
- Structured JSON output

Do not assume every model supports native JSON-schema enforcement. Validate model capabilities and implement appropriate fallbacks.

Initially evaluate inexpensive Qwen-family models and GPT-OSS models, but make model selection configuration-driven.

The goal is high extraction accuracy at minimal inference cost, not maximum model capability.

## 3. Architectural principles

Use a Go-first monorepo.

Prefer a modular monolith for Go business logic, with Python as the only separate language-specific service.

Avoid unnecessary microservices.

Separate:

1. Discovery
2. Crawling and parsing
3. Classification and extraction
4. Incident resolution
5. Persistence
6. Query/API

Design interfaces so these components can be tested independently.

Use dependency injection through constructors and interfaces where appropriate.

Keep the domain model independent of external API representations and database implementations.

Favor explicit data contracts over untyped maps.

All operations that may be retried must be idempotent.

## 4. Repository structure

Use the following as a starting point, modifying it when justified:

```text
collision-tracker/
├── cmd/
│   ├── api/
│   ├── worker/
│   ├── scheduler/
│   └── collisionctl/
├── internal/
│   ├── config/
│   ├── domain/
│   ├── discovery/
│   │   ├── gdelt/
│   │   ├── googlenews/
│   │   ├── rss/
│   │   └── registry/
│   ├── pipeline/
│   ├── crawler/
│   ├── classification/
│   │   └── prompts/
│   ├── incidents/
│   ├── geocoding/
│   ├── storage/
│   ├── api/
│   └── observability/
├── crawler/
│   ├── pyproject.toml
│   ├── src/collision_crawler/
│   │   ├── server.py
│   │   ├── fetcher.py
│   │   ├── renderer.py
│   │   ├── extractor.py
│   │   └── spiders/
│   └── tests/
├── proto/
│   └── crawler/v1/
├── db/
│   ├── migrations/
│   └── queries/
├── deploy/
│   ├── docker/
│   ├── helm/
│   └── local/
├── tests/
│   ├── fixtures/
│   ├── integration/
│   └── evaluations/
├── docs/
│   ├── architecture.md
│   ├── discovery.md
│   ├── data-model.md
│   ├── operations.md
│   └── decisions/
├── go.mod
├── buf.yaml
├── Makefile
└── README.md
```

## 5. Article discovery system

Discovery is a critical component.

Do not attempt to crawl the entire internet indiscriminately.

Implement multiple independent discovery providers.

### 5.1 GDELT

Implement a Go adapter for the GDELT DOC API.

Support:

- Multiple search queries
- Configurable lookback windows
- Geographic targeting where supported
- Result pagination or window partitioning where supported
- Rate limiting
- Retry and backoff
- Source attribution
- Deduplication
- API failure handling

Research and verify current GDELT API behavior and limitations before implementation. Do not assume arbitrary pagination, rate limits, or query capabilities.

### 5.2 Google News RSS

Implement a Go discovery adapter for Google News RSS search feeds.

Support configurable search queries and regions.

Resolve publisher article URLs when possible.

Do not rely on undocumented URL-decoding techniques without fallbacks and tests.

### 5.3 Direct publisher feeds

Support:

- RSS
- Atom
- News sitemaps
- Standard XML sitemaps
- Publisher news index pages
- Police department press-release pages

Use Go for RSS, Atom, and sitemap retrieval and parsing.

Use Python when HTML crawling or webpage parsing is required.

### 5.4 Source registry

Implement a configurable source registry.

Each source should define:

- Source identifier
- Name
- Source type
- URL
- Geographic coverage
- Polling frequency
- Enabled status
- Crawl policy
- Rate limits
- Last successful discovery
- Error state

Allow sources to be added without changing application code.

### 5.5 Search query families

Create configurable discovery queries covering:

- Pedestrians struck or killed
- Cyclists struck or killed
- Scooter riders injured or killed
- Motorcyclists injured or killed
- Hit-and-run collisions
- Vehicles striking wheelchair users
- Vehicles striking skateboarders
- Traffic fatalities involving vulnerable road users

Use synonyms and alternative phrasing.

Avoid assuming every relevant article contains the words "accident" or "crash."

### 5.6 Discovery scheduling

Run discovery periodically, initially every 20–30 minutes.

Use overlapping search windows to accommodate indexing delays.

Support daily and weekly backfills.

Deduplicate candidate URLs before scheduling article processing.

Preserve discovery provenance: an article may be found through several independent providers.

### 5.7 Discovery quality

Track:

- Articles discovered per source
- Unique URLs discovered
- Duplicate URL rate
- Discovery-to-publication delay
- Fetch success rate
- Relevant article percentage
- Unique incidents contributed per discovery provider

The system should make it possible to determine which discovery sources are actually valuable.

## 6. Python crawling service

Expose a versioned gRPC API.

Required operations:

- `FetchArticle`
- `DiscoverLinks`

### FetchArticle

Input:

- URL
- Rendering policy
- Optional source identifier
- Request timeout

Output:

- Requested URL
- Final URL
- Canonical URL
- Title
- Publisher
- Author, if available
- Publication timestamp, if available
- Article text
- Description
- Language
- HTTP status
- Content type
- Fetch timestamp
- Whether browser rendering was used
- Extraction status
- Error details

Use HTTP-only extraction by default.

Use Trafilatura for article extraction.

Fall back to Playwright when extraction fails because relevant content requires JavaScript.

Do not automatically launch Chromium for every article.

### DiscoverLinks

Input:

- Seed URL
- Allowed domains
- URL patterns
- Maximum pages
- Maximum crawl depth
- Maximum duration
- Rendering policy

Output:

- Discovered URLs
- Titles where available
- Discovery metadata

Use bounded crawling.

Do not implement unbounded site traversal.

### Security

Treat every discovered URL and webpage as untrusted.

Implement:

- URL scheme validation
- DNS resolution checks
- SSRF protection
- Blocking of loopback, link-local, private, and cluster-internal destinations
- Redirect validation
- Response-size limits
- Timeouts
- Domain restrictions
- Browser sandboxing
- Resource limits
- Per-domain rate limits

Prevent DNS rebinding and redirect-based bypasses.

Respect robots.txt and publisher access restrictions.

Do not bypass paywalls, authentication, CAPTCHAs, or other access controls.

Do not allow untrusted webpage content to influence crawler configuration or internal service access.

## 7. Classification and extraction

Implement all LLM-related logic in Go.

Use a two-stage approach where useful:

1. Determine whether the article contains a potentially qualifying collision.
2. Extract structured details about each incident.

Evaluate whether combining both stages into one LLM request is more cost-effective.

### Classification

Identify:

- Whether a collision occurred
- Whether a motor vehicle was involved
- Whether a vulnerable road user was involved
- Whether an injury or death occurred
- Whether the article is historical, hypothetical, or unrelated
- Whether multiple incidents are described
- Whether the article updates an earlier incident

Distinguish:

- Confirmed qualifying incidents
- Possible qualifying incidents
- Nonqualifying articles
- Insufficient information

### Structured extraction

For each incident, extract:

- Incident date and time
- Timezone, if known
- City
- County
- State
- Country
- Street address or intersection
- Location precision
- Vulnerable road user categories
- Vehicle categories
- Number of injured people
- Number of fatalities
- Injury severity
- Hit-and-run status
- Collision circumstances
- Whether the facts are confirmed or alleged
- Article references
- Supporting text excerpts

Do not infer unknown facts.

Distinguish article publication date from incident occurrence date.

Distinguish an injury from a confirmed fatality.

Do not infer fault or legal responsibility.

Store uncertainty explicitly.

Preserve supporting evidence for material extracted facts.

Treat LLM output as untrusted and validate it before persistence.

### Model evaluation

Create a labeled evaluation dataset.

Measure:

- Classification precision
- Classification recall
- Extraction field accuracy
- Date extraction accuracy
- Location extraction accuracy
- Severity accuracy
- Hallucination rate
- Average latency
- Input and output tokens
- Cost per article
- Cost per confirmed incident

Store model name, prompt version, schema version, and extraction timestamp for reproducibility.

## 8. Incident entity resolution

This is a critical correctness requirement.

Several articles may describe the same collision.

One article may describe multiple collisions.

Implement a many-to-many relationship between articles and incidents.

### Candidate matching

Use:

- Incident date proximity
- Geospatial proximity
- Street/intersection similarity
- City and county
- Vulnerable road user categories
- Vehicle categories
- Collision circumstances
- Other corroborating evidence

Use PostgreSQL/PostGIS to retrieve plausible candidate matches.

### Resolution

Implement deterministic matching rules first.

Use an LLM for ambiguous cases when necessary.

Support:

- Confirmed match
- Possible match requiring review
- Distinct incident
- Insufficient information

Avoid automatically merging incidents solely because they happened near the same location.

Maintain an audit trail of merges and corrections.

Support manually splitting incorrectly merged incidents.

### Incident updates

A collision may initially be reported as an injury and later as a fatality.

Support changes to:

- Fatality count
- Injury count
- Severity
- Location
- Incident time
- Verification status

Preserve historical observations rather than overwriting all prior information.

## 9. Database design

Create migrations for at least:

- `discovery_sources`
- `discovery_queries`
- `discovered_urls`
- `article_discoveries`
- `articles`
- `article_fetch_attempts`
- `article_extractions`
- `incidents`
- `incident_participants`
- `incident_articles`
- `incident_observations`
- `incident_events`

Use appropriate unique constraints and indexes.

Use PostGIS spatial indexes for incident locations.

Support unknown or approximate coordinates.

Store location precision and geocoding provenance.

Do not fabricate precise coordinates for articles that only identify a city.

Consider a raw extracted-text retention policy and publisher copyright constraints.

## 10. Temporal workflows

Implement the following workflows:

### DiscoveryWorkflow

- Execute discovery providers
- Normalize candidate URLs
- Persist discoveries
- Schedule article processing
- Track progress and errors

### ArticleWorkflow

- Fetch article through Python
- Persist normalized content
- Classify article
- Extract structured incident candidates
- Geocode locations
- Resolve existing incidents
- Persist results and provenance

### ReprocessWorkflow

- Select articles by model or prompt version
- Re-run classification and extraction
- Compare previous and new results
- Reconcile incident changes safely

### BackfillWorkflow

- Process historical discovery windows
- Support configurable date ranges
- Bound concurrency and API costs
- Checkpoint progress
- Support cancellation and resumption

Configure sensible activity retries, deadlines, and rate limits.

Avoid duplicating expensive LLM calls unnecessarily during retries.

Keep workflows deterministic.

## 11. HTTP API

Implement a Go REST API.

Suggested endpoints:

```text
GET /healthz
GET /readyz

GET /api/v1/incidents
GET /api/v1/incidents/{id}
GET /api/v1/incidents/{id}/sources

GET /api/v1/articles
GET /api/v1/articles/{id}

GET /api/v1/sources
GET /api/v1/stats
```

Support filtering incidents by:

- Date range
- Geography
- Bounding box
- Road user type
- Injury severity
- Fatalities
- Hit-and-run status
- Verification status

Implement pagination and predictable sorting.

Provide OpenAPI documentation.

Protect administrative and mutation endpoints with authentication.

Do not expose internal crawler or Temporal controls publicly without authorization.

## 12. Administrative CLI

Implement a Go CLI called `collisionctl`.

Suggested commands:

```bash
collisionctl discover --provider gdelt
collisionctl discover --provider googlenews

collisionctl fetch https://example.org/article
collisionctl classify https://example.org/article

collisionctl incidents list
collisionctl incidents show <id>

collisionctl backfill --from 2026-01-01 --to 2026-02-01

collisionctl reprocess --model-version <version>

collisionctl sources list
collisionctl sources validate
```

The CLI should be useful for development and debugging.

Provide human-readable and JSON output modes.

## 13. Kubernetes and container deployment

The target production environment is a lightweight ARM64 k3s cluster using Raspberry Pi 4B and Pi 5 nodes.

Produce multi-architecture container images for:

- linux/arm64
- linux/amd64

Create Dockerfiles for Go and Python components.

Use multi-stage builds.

Run containers as non-root where practical.

Provide Kubernetes health probes, resource requests and limits, and security contexts.

Initial deployments:

- `collision-api`
- `collision-worker`
- `collision-crawler`
- `collision-scheduler`, if a separate scheduler is justified

Avoid deploying redundant infrastructure already available in the cluster.

Use existing:

- Temporal
- PostgreSQL where appropriate
- LLM gateway
- VictoriaMetrics/Grafana

Provide a Helm chart under `deploy/helm`.

Make it suitable for GitOps management through Argo CD or Flux.

Use Kubernetes Secrets or external secret references for credentials.

Never commit real API keys.

Keep Playwright browser concurrency low and configurable.

Support disabling browser rendering entirely.

## 14. Observability

Expose Prometheus-compatible metrics.

Track:

- Discovery requests
- Discovered URLs
- Crawl attempts
- Crawl duration
- Crawl failures
- Browser fallback rate
- Article extraction failures
- Classification latency
- LLM request latency
- Token usage
- Estimated inference cost
- Qualifying article counts
- Incidents created
- Incidents updated
- Duplicate incidents detected
- Geocoding failures
- Temporal activity failures

Use structured JSON logging.

Propagate correlation identifiers across Go, Temporal, and Python gRPC calls.

Avoid placing full article bodies, API credentials, or unnecessary personal information in logs.

Provide a starter Grafana dashboard or documented PromQL queries.

## 15. Testing requirements

Implement:

- Go unit tests
- Python pytest tests
- gRPC contract tests
- PostgreSQL integration tests
- Temporal workflow tests
- Crawler tests using saved HTML fixtures
- End-to-end pipeline tests
- LLM response validation tests
- Incident deduplication tests

Tests must not require live publisher websites or paid LLM calls by default.

Use deterministic fixtures and mock implementations.

Create representative cases involving:

- Pedestrian fatality
- Injured bicyclist
- Scooter rider struck by vehicle
- Motorcyclist injury
- Hit-and-run
- Nonqualifying vehicle-only collision
- Nonqualifying single-bicycle fall
- Historical article
- Article describing multiple collisions
- Multiple articles describing one collision
- Injury later updated to fatality
- Missing or ambiguous incident location
- Conflicting source reports

Test idempotency and concurrent processing.

## 16. Local development

Provide Docker Compose or equivalent tooling for:

- PostgreSQL/PostGIS
- Temporal development server
- Python crawler
- Go worker
- Go API
- Mock LLM gateway

Provide Makefile targets for:

```bash
make generate
make lint
make test
make test-integration
make build
make docker-build
make dev-up
make dev-down
make migrate
```

Document local setup thoroughly.

A developer should be able to run the system locally without a Kubernetes cluster.

## 17. Implementation sequence

Implement incrementally, ensuring each milestone works before moving forward.

### Milestone 1 — Working vertical slice

Deliver:

- Go module
- Python package
- Protobuf contract
- Python gRPC crawler
- Trafilatura extraction
- Go gRPC client
- `collisionctl fetch`
- Unit tests
- Local container setup

Acceptance criterion:

A developer can provide an article URL and receive normalized article JSON.

### Milestone 2 — LLM extraction

Deliver:

- Configurable OpenAI-compatible gateway client
- Classification schema
- Extraction schema
- Pydantic-free Go validation
- Versioned prompts
- `collisionctl classify`
- Mock LLM tests
- Token and cost tracking

Acceptance criterion:

A developer can classify a saved article and receive validated incident candidates.

### Milestone 3 — Persistence and entity resolution

Deliver:

- PostgreSQL/PostGIS migrations
- Article persistence
- Incident persistence
- Incident-to-article relationships
- Candidate matching
- Deduplication
- Incident update history
- Integration tests

Acceptance criterion:

Multiple articles about the same collision produce one canonical incident, while distinct incidents remain separate.

### Milestone 4 — Automated discovery

Deliver:

- GDELT adapter
- Google News RSS adapter
- RSS/Atom adapter
- Source registry
- Query configuration
- URL normalization
- Discovery provenance
- Scheduled discovery

Acceptance criterion:

The system can automatically discover and enqueue new candidate articles without manual URLs.

### Milestone 5 — Durable processing

Deliver:

- Temporal workflows
- Retry policies
- Backfills
- Reprocessing
- Cancellation
- Concurrency controls

Acceptance criterion:

Processing survives worker restarts without losing work or creating duplicate incidents.

### Milestone 6 — Production deployment

Deliver:

- REST API
- ARM64/AMD64 container images
- Helm chart
- Kubernetes security settings
- Metrics
- Documentation
- End-to-end tests

Acceptance criterion:

The system can run continuously on an ARM64 k3s cluster and process newly discovered articles.

## 18. Design decisions requiring investigation

Before implementation, investigate and document:

1. Current GDELT API capabilities and limitations.
2. Google News RSS behavior and canonical URL resolution.
3. Whether Scrapy is necessary for a mostly URL-driven crawler or whether a simpler Python HTTP client is sufficient.
4. Trafilatura extraction quality and limitations.
5. Playwright ARM64 container/browser support.
6. gRPC versus HTTP/JSON for the Python boundary.
7. Temporal workflow granularity and history growth.
8. Efficient PostgreSQL/PostGIS entity resolution.
9. Model compatibility with structured output.
10. LLM gateway cost accounting and fallback behavior.
11. How to measure discovery recall, given that news coverage is inherently incomplete.
12. Publisher crawling restrictions and content retention policies.

Write concise architecture decision records for meaningful choices.

Do not blindly adopt every suggested library if a simpler or better-maintained alternative exists.

## 19. Engineering quality requirements

- Prefer simple, explicit implementations.
- Avoid unnecessary abstractions.
- Avoid premature distributed-system complexity.
- Make all configuration externalizable.
- Use structured errors.
- Handle partial failures.
- Respect cancellation and deadlines.
- Make retries safe.
- Use bounded queues and concurrency.
- Make data migrations repeatable.
- Document assumptions.
- Avoid inventing unsupported API capabilities.
- Do not silently discard malformed or ambiguous data.
- Never treat LLM-generated content as authoritative without validation.
- Preserve source attribution.
- Keep sensitive information out of logs.
- Respect publisher access restrictions.
- Do not fabricate successful test results.

## 20. Instructions for execution

Begin by:

1. Inspecting the repository and existing project files, if any.
2. Researching current library compatibility and relevant external API contracts.
3. Producing `docs/architecture.md`, `docs/data-model.md`, and a milestone implementation plan.
4. Identifying major technical risks and proposing mitigations.
5. Implementing Milestone 1 end-to-end.
6. Running all available tests.
7. Continuing through subsequent milestones as feasible.

Do not wait for approval between ordinary implementation steps.

When uncertain, make a reasonable engineering decision and document it.

Ask for clarification only when a decision materially changes the project's requirements, costs, security posture, or architecture and cannot be reasonably inferred.

Do not create fake integrations or pretend an external API was tested if it was not.

At the end of each milestone, report:

- What was implemented
- Files created or modified
- Tests executed and results
- Known limitations
- Remaining work
- How to run the completed functionality

The ultimate goal is a reliable, cost-efficient, continuously operating collision intelligence platform that is primarily written in Go, uses Python only for crawling and webpage extraction, and runs on lightweight Kubernetes infrastructure.

