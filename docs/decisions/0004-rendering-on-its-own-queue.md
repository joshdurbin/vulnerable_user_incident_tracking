# 0004: Browser rendering is a separate step on its own task queue

Status: accepted

## Context
Some publishers build the article text in the browser, so a plain HTTP fetch finds nothing. A headless browser fixes
that, but Chromium costs hundreds of MB per open page and about 1.5 GB of image, and a crash takes every page in that
process with it. The first design had the HTTP activity call a renderer in-process ("auto" / "always").

## Decision
- `render_article` is its own activity on its own task queue (`collision-render`), served by its own Deployment and image
  (`crawler` image, `--target render`). The crawl pods contain no browser and no Playwright.
- Escalation is a workflow decision, not an in-activity one. `fetchWithRendering` follows the rendering policy:
  `http_only` never renders; `auto` renders only when HTTP returned `NO_CONTENT`; `always` renders only.
  Both steps are visible, retried and timed out separately in Temporal.
- In `auto`, rendering can only help: a failed render, a render that finds nothing, or a missing pool keeps the HTTP
  result. The render activity has a 3 minute schedule-to-start limit so an absent pool cannot park articles.
- Off by default (`render.enabled`, `COLLISION_CRAWLER_RENDER_FALLBACK`). Turning it on adds the Deployment, its
  NetworkPolicy, a scrape target and a "not polling" alert.
- One Chromium process per pod, a fresh context per render, a semaphore for concurrent pages, and relaunch when the
  browser disconnects.

## Security
Chromium follows redirects (even a 3xx the worker hands back) without consulting a route handler for the next hop; a test
found a redirect reaching an address the policy forbids. So the worker follows redirects itself, validating every hop and
resolving DNS publicly, and gives the browser only the final response (with a `<base>` tag so relative links resolve).
Non-GET requests are refused. Chromium still resolves hostnames on its own, so the egress NetworkPolicy that denies
private ranges is part of the design, not an extra.

## Consequences
- A second image to build and a heavier pod to schedule; a Pi-class node may not suit it.
- Cluster-wide concurrent renders are replicas x `browserConcurrency`.
- Rendered text is stored like any article, with `rendered = true` on the fetch attempt.
