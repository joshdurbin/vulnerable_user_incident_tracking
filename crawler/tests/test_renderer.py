"""Real Chromium against local servers. Skipped when the browser is not installed
(`uv run playwright install chromium`)."""

import asyncio
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest
from temporalio.testing import ActivityEnvironment

from collision_crawler import renderer as renderer_mod
from collision_crawler.activities import FetchArticleRequest, RenderActivities
from collision_crawler.config import Config
from collision_crawler.renderer import Renderer
from collision_crawler.security import BlockedError, ParsedURL

TEXT = "A pedestrian was struck by a car at the intersection downtown on Tuesday evening. " * 6
SHELL = "<html><head><title>Shell</title></head><body><div id=app></div><script>%s</script></body></html>"


class Site:
    """Two local origins: `main` is allowed by the test policy, `private` stands in for an internal service."""

    def __init__(self):
        self.private_hits: list[str] = []
        site = self

        class Main(BaseHTTPRequestHandler):
            def log_message(self, *a): ...
            def do_GET(self):
                if self.path == "/js":
                    # The article only exists after this script runs.
                    body = SHELL % ("document.getElementById('app').innerHTML = '<article><h1>Crash downtown</h1><p>%s</p></article>'" % TEXT)
                elif self.path == "/static":
                    body = "<html><body><p>hi</p></body></html>"
                elif self.path == "/probe":
                    body = SHELL % f"fetch('http://127.0.0.1:{site.private_port}/secret').catch(()=>{{}}); document.getElementById('app').innerHTML = '<article><p>{TEXT}</p></article>'"
                elif self.path in ("/hop", "/chain"):
                    self.send_response(302); self.send_header("location", "/js" if self.path == "/hop" else "/hop2"); self.end_headers(); return
                elif self.path == "/hop2":
                    self.send_response(302); self.send_header("location", "/bounce"); self.end_headers(); return
                elif self.path == "/bounce":
                    self.send_response(302); self.send_header("location", f"http://127.0.0.1:{site.private_port}/secret"); self.end_headers(); return
                else:
                    self.send_response(404); self.end_headers(); return
                self.send_response(200); self.send_header("content-type", "text/html"); self.end_headers()
                self.wfile.write(body.encode())

        class Private(BaseHTTPRequestHandler):
            def log_message(self, *a): ...
            def do_GET(self):
                site.private_hits.append(self.path)
                self.send_response(200); self.end_headers(); self.wfile.write(b"secret")

        self.main, self.private = ThreadingHTTPServer(("127.0.0.1", 0), Main), ThreadingHTTPServer(("127.0.0.1", 0), Private)
        self.main_port, self.private_port = self.main.server_port, self.private.server_port
        for s in (self.main, self.private):
            threading.Thread(target=s.serve_forever, daemon=True).start()

    def url(self, path: str) -> str:
        return f"http://127.0.0.1:{self.main_port}{path}"

    def close(self):
        for s in (self.main, self.private):
            s.shutdown()


@pytest.fixture
def site(monkeypatch):
    s = Site()

    def allow_main_only(url, allowed_domains=None):
        from urllib.parse import urlsplit
        p = urlsplit(url)
        if p.scheme not in ("http", "https") or p.port != s.main_port:
            raise BlockedError(f"test policy blocks {url}")
        return ParsedURL(url=url, scheme=p.scheme, host=p.hostname, port=p.port)

    async def no_dns(host, port):
        return [host]

    monkeypatch.setattr(renderer_mod, "validate_url", allow_main_only)
    monkeypatch.setattr(renderer_mod, "resolve_public", no_dns)
    monkeypatch.setattr("collision_crawler.activities.validate_url", lambda url, *a, **k: ParsedURL(url, "http", "127.0.0.1", s.main_port))
    yield s
    s.close()


@pytest.fixture
async def rend():
    r = Renderer(Config(per_domain_min_interval_s=0.0, browser_concurrency=2))
    try:
        await r._get_browser()
    except Exception as exc:  # no browser installed
        pytest.skip(f"chromium unavailable: {exc}")
    yield r
    await r.aclose()


async def test_javascript_built_article_is_extracted(site, rend):
    acts = RenderActivities(rend)
    out = await ActivityEnvironment().run(acts.render_article, FetchArticleRequest(url=site.url("/js"), timeout_s=20))
    assert out.extraction_status == "OK" and out.rendered and out.http_status == 200
    assert "struck by a car" in out.text and out.final_url == site.url("/js")


async def test_page_with_no_article_is_no_content(site, rend):
    out = await ActivityEnvironment().run(RenderActivities(rend).render_article, FetchArticleRequest(url=site.url("/static"), timeout_s=20))
    assert out.extraction_status == "NO_CONTENT" and out.rendered


async def test_subresource_requests_to_blocked_hosts_are_not_made(site, rend):
    page = await rend.render(site.url("/probe"), 20)
    assert "struck by a car" in page.html  # the page rendered...
    assert site.private_hits == []         # ...but its script never reached the private origin


async def test_redirect_to_blocked_host_is_blocked(site, rend):
    out = await ActivityEnvironment().run(RenderActivities(rend).render_article, FetchArticleRequest(url=site.url("/bounce"), timeout_s=20))
    assert out.extraction_status == "BLOCKED"
    assert site.private_hits == []


async def test_browser_crash_is_survived_by_relaunch(site, rend):
    await rend._browser.close()  # simulates Chromium dying between renders
    page = await rend.render(site.url("/js"), 20)
    assert "struck by a car" in page.html


async def test_concurrent_renders_share_one_browser(site, rend):
    pages = await asyncio.gather(*(rend.render(site.url("/js"), 20) for _ in range(4)))
    assert all("struck by a car" in p.html for p in pages)


async def test_allowed_redirect_is_followed_and_reported(site, rend):
    out = await ActivityEnvironment().run(RenderActivities(rend).render_article, FetchArticleRequest(url=site.url("/hop"), timeout_s=20))
    assert out.extraction_status == "OK" and out.final_url == site.url("/js")


async def test_redirect_chain_is_checked_at_every_hop(site, rend):
    # /chain -> /hop2 -> /bounce -> (private origin): only the last hop is bad.
    out = await ActivityEnvironment().run(RenderActivities(rend).render_article, FetchArticleRequest(url=site.url("/chain"), timeout_s=20))
    assert out.extraction_status == "BLOCKED" and site.private_hits == []
