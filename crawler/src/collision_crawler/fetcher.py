"""Safe, bounded, polite page retrieval plus article extraction."""

from __future__ import annotations

import asyncio
import logging
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from urllib.parse import urljoin

import httpx

from .config import Config
from .metrics import Metrics
from .extractor import Extracted, extract_article
from .ratelimit import DomainLimiter
from .robots import RobotsCache
from .safe_http import make_client
from .security import BlockedError, validate_url

log = logging.getLogger(__name__)

HTML_TYPES = ("text/html", "application/xhtml+xml", "text/plain")
MIN_ARTICLE_CHARS = 200

OK = "OK"
NO_CONTENT = "NO_CONTENT"
FETCH_FAILED = "FETCH_FAILED"
BLOCKED = "BLOCKED"
ROBOTS = "ROBOTS_DISALLOWED"
UNSUPPORTED = "UNSUPPORTED"


@dataclass
class Page:
    requested_url: str
    final_url: str = ""
    status: int = 0
    content_type: str = ""
    html: str = ""
    error: str = ""
    outcome: str = OK  # one of the constants above, before extraction


@dataclass
class Article:
    page: Page
    extracted: Extracted = field(default_factory=Extracted)
    rendered: bool = False
    extraction_status: str = OK
    fetched_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))


class Fetcher:
    def __init__(self, cfg: Config, client: httpx.AsyncClient | None = None, metrics: Metrics | None = None):
        self.cfg = cfg
        self.metrics = metrics or Metrics()
        self._client = client or make_client(cfg.user_agent, cfg.default_timeout_s)
        self._limiter = DomainLimiter(cfg.per_domain_min_interval_s)
        self._robots = RobotsCache(self._robots_get, cfg.user_agent, cfg.robots_ttl_s)
        self._sem = asyncio.Semaphore(cfg.max_concurrent_fetches)

    async def aclose(self) -> None:
        await self._client.aclose()

    async def _robots_get(self, url: str) -> tuple[int, str]:
        page = await self._get(url, check_robots=False, timeout=10.0)
        return page.status, page.html

    async def _get(self, url: str, *, check_robots: bool = True, timeout: float | None = None,
                   allowed_domains: list[str] | None = None) -> Page:
        """GET with hop-by-hop redirect validation and a hard body-size cap."""
        page = Page(requested_url=url)
        current = url
        for _ in range(self.cfg.max_redirects + 1):
            try:
                parsed = validate_url(current, allowed_domains)
            except BlockedError as exc:
                page.outcome, page.error, page.final_url = BLOCKED, str(exc), current
                return page
            if check_robots and not await self._robots.allowed(parsed.scheme, parsed.host, current):
                page.outcome, page.error, page.final_url = ROBOTS, "disallowed by robots.txt", current
                return page
            waited = time.monotonic()
            await self._limiter.wait(parsed.host)
            self.metrics.domain_wait(time.monotonic() - waited)
            try:
                async with self._sem, self._client.stream("GET", current, timeout=timeout or self.cfg.default_timeout_s) as resp:
                    page.final_url, page.status = current, resp.status_code
                    if check_robots and not resp.is_redirect:  # robots.txt lookups are not page fetches
                        self.metrics.http(resp.status_code)
                    if resp.is_redirect and "location" in resp.headers:
                        current = urljoin(current, resp.headers["location"])
                        continue
                    ctype = resp.headers.get("content-type", "").split(";")[0].strip().lower()
                    page.content_type = ctype
                    if ctype and not ctype.startswith(HTML_TYPES):
                        page.outcome, page.error = UNSUPPORTED, f"content type {ctype}"
                        return page
                    body = bytearray()
                    async for chunk in resp.aiter_bytes():
                        body.extend(chunk)
                        if len(body) > self.cfg.max_response_bytes:
                            page.outcome, page.error = UNSUPPORTED, "response too large"
                            return page
                    if check_robots:
                        self.metrics.body_bytes(len(body))
                    page.html = bytes(body).decode(resp.encoding or "utf-8", errors="replace")
                    if resp.status_code >= 400:
                        page.outcome, page.error = FETCH_FAILED, f"http {resp.status_code}"
                    return page
            except BlockedError as exc:
                page.outcome, page.error = BLOCKED, str(exc)
                return page
            except (httpx.HTTPError, OSError) as exc:
                page.outcome, page.error = FETCH_FAILED, f"{type(exc).__name__}: {exc}"
                return page
        page.outcome, page.error = FETCH_FAILED, "too many redirects"
        return page

    async def fetch_page(self, url: str, timeout: float | None = None,
                         allowed_domains: list[str] | None = None) -> Page:
        return await self._get(url, timeout=timeout, allowed_domains=allowed_domains)

    async def fetch_article(self, url: str, timeout: float | None = None) -> Article:
        page = await self._get(url, timeout=timeout)
        art = Article(page=page)
        if page.outcome != OK:
            art.extraction_status = page.outcome
            return art
        art.extracted = extract_article(page.html, page.final_url)
        if len(art.extracted.text) < MIN_ARTICLE_CHARS:
            art.extraction_status = NO_CONTENT
        return art
