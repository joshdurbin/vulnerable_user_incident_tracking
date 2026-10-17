from pathlib import Path

import httpx
import pytest

from collision_crawler.config import Config
from collision_crawler.fetcher import Fetcher

FIX = Path(__file__).parent / "fixtures"


def fixture_text(name: str) -> str:
    return (FIX / name).read_text()


def make_fetcher(routes: dict, **cfg_kw) -> Fetcher:
    """Fetcher backed by an in-memory transport. routes: url -> (status, headers, body)."""
    calls: list[str] = []

    def handler(req: httpx.Request) -> httpx.Response:
        url = str(req.url)
        calls.append(url)
        if url in routes:
            status, headers, body = routes[url]
            return httpx.Response(status, headers=headers, content=body)
        if url.endswith("/robots.txt"):
            return httpx.Response(404)
        return httpx.Response(404, content=b"nope")

    cfg = Config(per_domain_min_interval_s=0.0, **cfg_kw)
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler), follow_redirects=False)
    f = Fetcher(cfg, client=client)
    f.calls = calls  # type: ignore[attr-defined]
    return f


@pytest.fixture
def html_headers():
    return {"content-type": "text/html; charset=utf-8"}
