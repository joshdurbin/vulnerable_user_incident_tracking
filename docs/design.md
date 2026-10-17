# Design

A platform that finds news reports of motor-vehicle collisions involving vulnerable road users, extracts
structured facts with an LLM, merges reports of the same collision into one incident, and shows the result on a map
with trends and a review queue.

Decisions and their reasons are in `docs/decisions/`. The delivery plan is `docs/implementation-plan.md`.

## Principles

- **Evidence first.** Every incident is derived from per-article observations that are kept verbatim. Unknown is
  stored as unknown (NULL), never guessed. Coordinates exist only when a geocoder returned them.
- **Orchestration is Temporal's job.** Retries, timeouts, scheduling, fan-out, human-in-the-loop and long timers are
  workflows. No second RPC layer sits between Go and Python.
- **State lives in PostgreSQL.** Workflow history carries IDs and small summaries only.
- **Cheap to retry, expensive to repeat.** A stored extraction is reused for the same model, prompt and schema, so
  retries and replays never pay for the model twice.
- **Untrusted input.** Web content is fetched behind an SSRF guard, and model output is validated before it is used.
- **Use libraries.** Official SDKs and mainstream libraries over hand-written equivalents.

## System

Four containers of our own, plus services that already run in the cluster. Access is tailnet-only.

```mermaid
flowchart LR
  user([Browser<br/>on tailnet]) --> web

  subgraph ours[Our containers]
    web[web<br/>nginx + React SPA]
    api[api<br/>Go, REST + OpenAPI]
    worker[worker<br/>Go: workflows + activities]
    crawler[crawler<br/>Python Temporal worker]
  end

  subgraph cluster[Existing cluster services]
    temporal[(Temporal<br/>namespace: collisions)]
    pg[(PostgreSQL + PostGIS<br/>CloudNativePG)]
    bifrost[Bifrost<br/>LLM gateway]
  end

  web -- "/v1" --> api
  api --> pg
  api -- "updates, schedule control" --> temporal
  worker <--> temporal
  crawler <--> temporal
  worker --> pg
  worker -- "OpenAI-compatible API" --> bifrost
  bifrost --> or[OpenRouter]
  crawler -- "HTTPS, SSRF-guarded" --> web_pages[(News sites,<br/>feeds, APIs)]
  worker -- "discovery HTTPS" --> web_pages
```

Go and Python never call each other directly. The Python worker serves two activities (`fetch_article`,
`discover_links`) on its own task queue, and Go workflows call them by name. Large payloads (article text) are
compressed by a zlib payload codec implemented on both sides.

## Task queues

Each queue has its own worker, concurrency limit and rate limit, so a slow LLM or a polite crawler cannot starve
database work.

```mermaid
flowchart TB
  subgraph go[Go worker process]
    wq[collision-workflow<br/>workflow tasks]
    lq[collision-llm<br/>Classify]
    dq[collision-db<br/>Find/Store article, Geocode,<br/>Resolve, Notify, reviews]
    disq[collision-discovery<br/>DiscoverSource, FollowUp<br/>single replica: provider limits are per process]
  end
  subgraph py[Python worker process]
    cq[collision-crawl<br/>fetch_article, discover_links]
  end
  wq -. schedules activities on .-> lq
  wq -.-> dq
  wq -.-> disq
  wq -.-> cq
```

## Discovery

Discovery sits behind one interface with an implementation per source type. A provider only finds candidate URLs;
persistence, normalization and deduplication are shared.

```mermaid
classDiagram
  class Provider {
    <<interface>>
    Type() string
    Discover(ctx, Request) Result
  }
  Provider <|.. GDELT : gdelt
  Provider <|.. GoogleNews : googlenews
  Provider <|.. Feed : rss, atom
  Provider <|.. Sitemap : sitemap, news_sitemap
  Provider <|.. CrawlLinks : index_page, police_press

  class Registry { maps source type to Provider }
  class Service {
    Run(source, window)
    provider -> normalize -> dedupe -> persist
  }
  Registry --> Provider
  Service --> Registry
  Service --> Repository
  class Repository { <<interface>> pgrepo, memory }
  CrawlLinks ..> Crawler : via Temporal (discover_links)
  GoogleNews ..> Resolver : opt-in batchexecute
```

- **GDELT** and **Google News** are search providers driven by configured queries. GDELT allows one request per 5
  seconds, which is enforced with a limiter and limiter-aware retries. Google News links are opaque, so resolving them
  to publisher URLs is an opt-in step that relies on an undocumented endpoint.
- **Feed**, **Sitemap** and **CrawlLinks** read a specific source's URL.
- Every sighting is recorded (`article_discoveries`), so source quality can be measured: duplicates, exclusive finds,
  delay, fetch success and relevance.

## Durable pipeline

```mermaid
flowchart TB
  sched[Temporal Schedule<br/>per source, overlap: skip] --> dw[DiscoveryWorkflow]
  bf[BackfillWorkflow<br/>date windows x sources] --> dw
  dw -- "child per URL<br/>workflow ID = hash of URL<br/>abandon on parent close" --> aw[ArticleWorkflow]
  fu[IncidentWorkflow<br/>follow-up search] --> aw
  aw --> iw[IncidentWorkflow<br/>one per incident]
  rw[ReprocessWorkflow] --> raw[ReprocessArticleWorkflow] --> iw
```

### One article

```mermaid
sequenceDiagram
  autonumber
  participant W as ArticleWorkflow
  participant DB as Go activities (db)
  participant C as Python crawler
  participant L as Classify (llm queue)
  participant I as IncidentWorkflow

  W->>DB: FindArticle(url)
  alt already stored
    DB-->>W: article id (skip fetch)
  else new
    W->>C: fetch_article(url)
    Note over C: SSRF guard, robots.txt, size cap,<br/>per-domain limit, trafilatura
    C-->>W: article (compressed payload)
    W->>DB: StoreArticle (text kept 90 days)
  end
  W->>L: Classify(article id)
  Note over L: reuse stored extraction for same<br/>model + prompt + schema, else call LLM,<br/>validate against schema and evidence quotes
  alt nonqualifying, insufficient, or budget exceeded
    L-->>W: stop (clean outcome, not a failure)
  else candidates found
    loop each candidate
      W->>DB: Geocode (no-op until a geocoder is chosen)
      W->>DB: ResolveIncident
      W->>I: SignalWithStart(article linked)
    end
  end
```

Retry policy differs by step: crawl and DB steps retry with backoff; classification retries transient gateway errors
but treats "budget exceeded" and "invalid output" as terminal outcomes recorded on the workflow, not failures.

### Classification

```mermaid
flowchart LR
  a[Article text] --> p[Versioned prompt v1<br/>text inside delimiters,<br/>treated as data]
  p --> m{Primary model}
  m -- "invalid output<br/>or error" --> f[Fallback model]
  m --> v
  f --> v[Validate]
  v --> s[JSON schema]
  v --> e[Evidence quotes must appear<br/>in the source text]
  v --> r[(article_extractions)]
```

The gateway client probes structured-output support and downgrades `json_schema` to `json_object` to prompt-only.
Token use and cost are tracked per call, with an optional spend limit.

## Entity resolution

Resolution runs in PostgreSQL inside one transaction, under an advisory lock per state, so parallel workers cannot
create duplicates of the same collision. It is idempotent per (extraction, candidate).

```mermaid
flowchart TB
  c[Candidate from an article] --> f[Find nearby candidates<br/>same state, date window,<br/>PostGIS distance or city/county/street similarity]
  f --> sc[Score each<br/>date .30, place .35, victim .20,<br/>outcome .10, vehicle .05]
  sc --> d{Best score}
  d -- ">= 0.80, known date, place >= 0.6,<br/>victim overlap, no fact conflict" --> auto[Attach to existing incident]
  d -- "two near-equal automatic matches<br/>or 0.55 to 0.80" --> rev[Create new incident<br/>and queue a review]
  d -- "< 0.55" --> new[Create new incident]
  auto --> fold[Fold all observations<br/>into canonical fields]
  new --> fold
  rev --> fold
```

Folding rules: counts only go up and a later, lower count is logged as a conflict; a fact conflict (for example
hit-and-run yes versus no) forces review instead of an automatic match.

## Incident lifecycle

Each incident has a long-lived workflow. Resolution correctness comes from the database lock; the workflow adds
durable timers, human decisions, a status query and visibility.

```mermaid
stateDiagram-v2
  [*] --> Waiting: SignalWithStart(article linked)
  Waiting --> Waiting: article-linked signal
  Waiting --> FollowingUp: timer fires (48h, then 7d)
  FollowingUp --> Waiting: targeted search, new URLs become article workflows
  Waiting --> Idle: follow-ups done
  Idle --> Idle: article-linked signal
  Idle --> [*]: 30 days without news
  Waiting --> Merged: decide-review update, approve
  Merged --> [*]: incident folded into the target
  Waiting --> ContinueAsNew: history getting long
  ContinueAsNew --> Waiting: carries counters and first-linked time
```

### Human review

A possible duplicate waits in the review queue. The decision is a validated Temporal Update delivered with
update-with-start, so it works even if the incident's workflow is not running.

```mermaid
sequenceDiagram
  autonumber
  actor R as Reviewer
  participant UI as Web UI
  participant API as API
  participant T as Temporal
  participant W as IncidentWorkflow
  participant DB as PostgreSQL

  R->>UI: Merge or keep separate (name + admin token)
  UI->>API: POST /v1/reviews/{id}/decision
  API->>API: check bearer token, review still pending
  API->>T: update-with-start (decide-review)
  T->>W: start if needed, deliver update
  W->>W: validate shape, wait for open reviews to load, check pending
  W->>DB: ApproveReview or RejectReview (one transaction)
  W-->>API: outcome (target incident)
  API-->>UI: 200 (or 409 if already decided)
```

### Backfill and reprocess

```mermaid
stateDiagram-v2
  [*] --> Running
  Running --> Paused: pause signal
  Paused --> Running: resume signal
  Running --> Cancelled: cancel signal
  Running --> BudgetReached: article budget spent
  Running --> Running: next (window, source) step<br/>Continue-As-New every 40 steps
  Running --> Done: all windows covered
```

A backfill walks date windows across sources, answers a progress query, and bounds cost with a maximum number of
article workflows. Reprocess pages through retained articles that lack an extraction for the current model, prompt
and schema, compares old and new results, withdraws what the old extraction contributed, and resolves the new one.

## Data model

```mermaid
erDiagram
  discovery_sources ||--o{ article_discoveries : found_by
  discovery_queries ||--o{ article_discoveries : found_by
  discovered_urls ||--o{ article_discoveries : sighted
  discovered_urls ||--o| articles : becomes
  discovered_urls ||--o{ article_fetch_attempts : tried
  articles ||--o{ article_extractions : classified_as
  article_extractions ||--o{ incident_articles : resolves_to
  articles ||--o{ incident_articles : supports
  incidents ||--o{ incident_articles : has
  incidents ||--o{ incident_observations : built_from
  articles ||--o{ incident_observations : reported_in
  incidents ||--o{ incident_events : audited_by
  incidents ||--o{ incident_participants : involves
  incidents ||--o{ match_reviews : candidate_or_new

  articles {
    uuid id PK
    text status "fetched, no_content, blocked, failed"
    text body_text "nulled after retention"
    timestamptz text_expires_at
    text content_hash
  }
  article_extractions {
    uuid id PK
    text model
    text prompt_version
    text schema_version
    jsonb result "validated extraction"
    numeric cost_usd
  }
  incidents {
    uuid id PK
    date occurred_on "NULL = unknown"
    geography geom "NULL unless geocoded"
    int injured_count "NULL = not reported"
    int fatality_count
    text severity
    text verification_status "unverified, corroborated, reviewed, withdrawn"
    uuid merged_into "soft merge"
  }
  incident_observations {
    uuid incident_id FK
    uuid article_id FK
    jsonb data "candidate as extracted"
  }
  incident_events {
    text event_type "created, updated, conflict, merged, split, review, withdrawn"
    text actor
  }
  match_reviews {
    uuid new_incident_id FK
    uuid candidate_incident_id FK
    real score
    text status "pending, approved, rejected"
  }
```

An incident's canonical fields are always recomputed from its observations, so merge, split and reprocessing are
safe operations. Reprocessing can remove all support for an incident, which is then withdrawn (hidden, kept for
audit) rather than deleted.

## API and web

The REST API is defined first in `api/openapi.yaml`. Server code and the web client's types are generated from it,
and requests are validated against it, including the bearer scheme on the few POST routes. Reads are open on the
tailnet; admin routes need a token and are disabled when none is configured.

```mermaid
flowchart LR
  spec[api/openapi.yaml] --> gen1[oapi-codegen<br/>Go strict server]
  spec --> gen2[openapi-typescript<br/>web types]
  spec --> val[request validation<br/>at runtime]
  gen1 --> api
  gen2 --> web
  val --> api
```

| Page | Data | Temporal feature shown |
|---|---|---|
| Map | clustered incidents with filters kept in the URL | none |
| Trends | summary, time series, breakdowns | none |
| Incident detail | sources, per-article observations, audit trail | link to the incident's workflow |
| Review queue | side-by-side pairs with differences highlighted | decision as a validated Update |
| Operations | source quality, spend, counters | schedule pause, resume, run now |

## Security

- **Crawler:** every resolved IP is validated and the connection is pinned to the checked IP (defeats DNS
  rebinding); redirects are re-validated per hop; robots.txt, size caps and per-domain limits apply; page content is
  never logged.
- **LLM:** article text is data, wrapped in delimiters and escaped. Output is schema-validated and each evidence
  quote must be found in the source text; "verified" is set by our code, never the model.
- **Privacy:** no names or ages are stored for people involved in a collision.
- **Admin:** shared bearer token compared in constant time, kept in `sessionStorage` in the browser. Reviewer identity
  is a typed name recorded in the audit trail, not an authenticated identity.
- **Network:** tailnet-only exposure; in-cluster egress is default-deny with explicit allows (infra repo).

## Not built yet

- Helm chart, Argo applications, Grafana dashboard and runbooks (Phase 5), and the metrics endpoints that
  VictoriaMetrics would scrape.
- The infra changes in the cluster repo: PostGIS image, `collisions` database and Temporal namespace, network
  policies, secrets.
- A geocoder (incidents currently match on street, city and county text), a monthly LLM budget stored in the
  database, a Temporal codec server, a browser-rendering fallback for script-heavy pages, and saga-style
  compensation for merge and split (they are single database transactions today).
