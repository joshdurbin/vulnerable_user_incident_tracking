"""Bounded link discovery. Breadth-first, same-allow-list, hard limits."""

from __future__ import annotations

import re
import time
from dataclasses import dataclass
from urllib.parse import urldefrag, urljoin

from bs4 import BeautifulSoup

from .fetcher import OK, Fetcher


@dataclass
class Link:
    url: str
    title: str
    found_on: str
    depth: int


@dataclass
class LinkResult:
    links: list[Link]
    pages_fetched: int
    truncated: bool
    error: str = ""


def extract_links(html: str, base_url: str) -> list[tuple[str, str]]:
    soup = BeautifulSoup(html, "lxml")
    out = []
    for a in soup.find_all("a", href=True):
        href = urldefrag(urljoin(base_url, a["href"]))[0]
        out.append((href, " ".join(a.get_text().split())[:300]))
    return out


async def discover(f: Fetcher, seed: str, allowed_domains: list[str], patterns: list[str],
                   max_pages: int, max_depth: int, max_duration_s: float) -> LinkResult:
    max_pages = max(1, min(max_pages or 20, 100))
    max_depth = max(0, min(max_depth if max_depth is not None else 1, 3))
    deadline = time.monotonic() + min(max_duration_s or 60.0, 300.0)
    compiled = [re.compile(p) for p in patterns]
    seen = {seed}
    found: dict[str, Link] = {}
    frontier = [(seed, 0)]
    fetched, truncated = 0, False
    while frontier:
        url, depth = frontier.pop(0)
        if fetched >= max_pages or time.monotonic() > deadline:
            truncated = True
            break
        page = await f.fetch_page(url, allowed_domains=allowed_domains or None)
        fetched += 1
        if page.outcome != OK:
            if url == seed:
                return LinkResult([], fetched, False, page.error)
            continue
        for href, title in extract_links(page.html, page.final_url):
            if href in seen or not href.startswith(("http://", "https://")):
                continue
            if allowed_domains and not any(
                (h := href.split("/")[2].split(":")[0].lower()) == d or h.endswith("." + d) for d in allowed_domains
            ):
                continue
            seen.add(href)
            if not compiled or any(c.search(href) for c in compiled):
                found[href] = Link(href, title, page.final_url, depth + 1)
            if depth + 1 <= max_depth:
                frontier.append((href, depth + 1))
    return LinkResult(list(found.values()), fetched, truncated)
