import pytest

from .conftest import fixture_text, make_fetcher

H = {"content-type": "text/html"}
ART = "https://news.example.com/a"


async def test_fetch_ok(html_headers):
    f = make_fetcher({ART: (200, html_headers, fixture_text("pedestrian_fatality.html"))})
    art = await f.fetch_article(ART)
    assert art.extraction_status == "OK"
    assert art.page.status == 200 and not art.rendered
    assert "pronounced dead" in art.extracted.text


async def test_redirect_followed_and_final_url_recorded(html_headers):
    f = make_fetcher({
        "http://news.example.com/old": (301, {"location": "https://news.example.com/a"}, b""),
        ART: (200, html_headers, fixture_text("pedestrian_fatality.html")),
    })
    art = await f.fetch_article("http://news.example.com/old")
    assert art.page.final_url == ART and art.extraction_status == "OK"


@pytest.mark.parametrize("target", [
    "http://169.254.169.254/latest/meta-data", "http://127.0.0.1:8080/admin", "file:///etc/passwd",
    "http://redis-master.redis.svc.cluster.local/",
])
async def test_redirect_to_internal_is_blocked(target):
    f = make_fetcher({ART: (302, {"location": target}, b"")})
    art = await f.fetch_article(ART)
    assert art.extraction_status == "BLOCKED"
    assert not [c for c in f.calls if "169.254" in c or "127.0.0.1" in c]


async def test_redirect_loop_bounded():
    f = make_fetcher({ART: (302, {"location": ART}, b"")})
    art = await f.fetch_article(ART)
    assert art.extraction_status == "FETCH_FAILED" and "redirect" in art.page.error


async def test_robots_disallow_respected():
    f = make_fetcher({
        "https://news.example.com/robots.txt": (200, {"content-type": "text/plain"}, "User-agent: *\nDisallow: /a\n"),
        ART: (200, H, fixture_text("pedestrian_fatality.html")),
    })
    art = await f.fetch_article(ART)
    assert art.extraction_status == "ROBOTS_DISALLOWED"
    assert ART not in f.calls  # page never requested


async def test_robots_server_error_fails_closed():
    f = make_fetcher({"https://news.example.com/robots.txt": (503, {}, b""), ART: (200, H, fixture_text("pedestrian_fatality.html"))})
    assert (await f.fetch_article(ART)).extraction_status == "ROBOTS_DISALLOWED"


async def test_non_html_unsupported():
    f = make_fetcher({ART: (200, {"content-type": "application/pdf"}, b"%PDF")})
    assert (await f.fetch_article(ART)).extraction_status == "UNSUPPORTED"


async def test_oversize_unsupported():
    f = make_fetcher({ART: (200, H, "x" * 5000)}, max_response_bytes=1000)
    art = await f.fetch_article(ART)
    assert art.extraction_status == "UNSUPPORTED" and "large" in art.page.error


async def test_http_error_status():
    f = make_fetcher({ART: (500, H, "boom")})
    art = await f.fetch_article(ART)
    assert art.extraction_status == "FETCH_FAILED" and art.page.status == 500


async def test_thin_page_is_no_content_and_no_browser_without_renderer():
    f = make_fetcher({ART: (200, H, fixture_text("thin_js_page.html"))})
    art = await f.fetch_article(ART, rendering="AUTO")
    assert art.extraction_status == "NO_CONTENT" and not art.rendered
