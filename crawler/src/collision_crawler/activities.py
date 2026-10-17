"""Temporal activities. They own no business logic and no persistence: fetch and
parse untrusted web content, return plain data. Failures that retrying cannot fix
(bad input) are non-retryable ApplicationErrors; network and policy outcomes are
returned as an extraction status so the workflow records them instead of retrying.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field

from temporalio import activity
from temporalio.exceptions import ApplicationError

from .fetcher import Fetcher
from .links import discover

log = logging.getLogger("collision_crawler")

FETCH_ARTICLE = "fetch_article"
DISCOVER_LINKS = "discover_links"
_RENDER = {"", "http_only", "auto", "always"}


@dataclass
class FetchArticleRequest:
    url: str = ""
    rendering: str = "http_only"  # http_only | auto | always
    source_id: str = ""
    timeout_s: float = 0.0
    correlation_id: str = ""


@dataclass
class ArticleResponse:
    requested_url: str = ""
    final_url: str = ""
    canonical_url: str = ""
    title: str = ""
    publisher: str = ""
    author: str = ""
    published_at: str | None = None  # RFC 3339
    text: str = ""
    description: str = ""
    language: str = ""
    http_status: int = 0
    content_type: str = ""
    fetched_at: str = ""
    rendered: bool = False
    extraction_status: str = "FETCH_FAILED"
    error_detail: str = ""


@dataclass
class DiscoverLinksRequest:
    seed_url: str = ""
    allowed_domains: list[str] = field(default_factory=list)
    url_patterns: list[str] = field(default_factory=list)
    max_pages: int = 10
    max_depth: int = 1
    max_duration_s: float = 60.0
    rendering: str = "http_only"
    correlation_id: str = ""


@dataclass
class DiscoveredLink:
    url: str = ""
    title: str = ""
    found_on: str = ""
    depth: int = 0


@dataclass
class DiscoverLinksResponse:
    links: list[DiscoveredLink] = field(default_factory=list)
    pages_fetched: int = 0
    truncated: bool = False
    error_detail: str = ""


class CrawlerActivities:
    def __init__(self, fetcher: Fetcher):
        self._f = fetcher

    @activity.defn(name=FETCH_ARTICLE)
    async def fetch_article(self, req: FetchArticleRequest) -> ArticleResponse:
        if not req.url:
            raise ApplicationError("url is required", type="InvalidArgument", non_retryable=True)
        if req.rendering not in _RENDER:
            raise ApplicationError(f"invalid rendering {req.rendering!r}", type="InvalidArgument", non_retryable=True)
        # Never log page content; the URL is enough for tracing.
        log.info("fetch cid=%s source=%s url=%s", req.correlation_id, req.source_id, req.url)
        art = await self._f.fetch_article(req.url, (req.rendering or "http_only").upper(), req.timeout_s or None)
        e, p = art.extracted, art.page
        return ArticleResponse(
            requested_url=req.url, final_url=p.final_url, canonical_url=e.canonical_url, title=e.title,
            publisher=e.publisher, author=e.author,
            published_at=e.published_at.isoformat() if e.published_at else None,
            text=e.text, description=e.description, language=e.language, http_status=p.status,
            content_type=p.content_type, fetched_at=art.fetched_at.isoformat(), rendered=art.rendered,
            extraction_status=art.extraction_status, error_detail=p.error,
        )

    @activity.defn(name=DISCOVER_LINKS)
    async def discover_links(self, req: DiscoverLinksRequest) -> DiscoverLinksResponse:
        if not req.seed_url:
            raise ApplicationError("seed_url is required", type="InvalidArgument", non_retryable=True)
        try:
            res = await discover(self._f, req.seed_url, req.allowed_domains, req.url_patterns,
                                 req.max_pages, req.max_depth, req.max_duration_s or 60.0)
        except Exception as exc:  # bad regex etc. is the caller's mistake, not a transient failure
            raise ApplicationError(f"{type(exc).__name__}: {exc}", type="InvalidArgument", non_retryable=True) from exc
        return DiscoverLinksResponse(
            links=[DiscoveredLink(l.url, l.title, l.found_on, l.depth) for l in res.links],
            pages_fetched=res.pages_fetched, truncated=res.truncated, error_detail=res.error,
        )
