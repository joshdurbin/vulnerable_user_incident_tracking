"""Article extraction from HTML using trafilatura. Pure function, no I/O."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone

import re

import trafilatura
from trafilatura.metadata import extract_metadata


@dataclass
class Extracted:
    title: str = ""
    publisher: str = ""
    author: str = ""
    published_at: datetime | None = None
    text: str = ""
    description: str = ""
    language: str = ""
    canonical_url: str = ""


def _parse_date(value: str | None) -> datetime | None:
    if not value:
        return None
    try:
        dt = datetime.fromisoformat(value)
    except ValueError:
        return None
    return dt if dt.tzinfo else dt.replace(tzinfo=timezone.utc)


_HTML_LANG = re.compile(r'<html[^>]*\slang=["\']([A-Za-z-]{2,10})["\']', re.I)


def _clean_author(value: str | None) -> str:
    """Trafilatura sometimes returns scraped junk (e.g. citation fragments); drop implausible values."""
    if not value or len(value) > 100 or any(c in value for c in '"<>{}[]'):
        return ""
    return value.strip()


def extract_article(html: str, url: str) -> Extracted:
    text = trafilatura.extract(
        html,
        url=url,
        include_comments=False,
        include_tables=False,
        favor_precision=True,
        deduplicate=False,  # trafilatura's dedupe cache is process-global; would drop re-fetched articles
    ) or ""
    meta = extract_metadata(html, default_url=url)
    out = Extracted(text=text.strip())
    if meta is not None:
        out.title = meta.title or ""
        out.publisher = meta.sitename or ""
        out.author = _clean_author(meta.author)
        out.description = meta.description or ""
        out.language = meta.language or ""
        out.canonical_url = meta.url or ""
        out.published_at = _parse_date(meta.date)
    if not out.language and (m := _HTML_LANG.search(html[:4096])):
        out.language = m.group(1).split("-")[0].lower()
    return out
