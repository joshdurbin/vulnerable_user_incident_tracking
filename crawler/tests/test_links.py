from collision_crawler.links import discover, extract_links

from .conftest import fixture_text, make_fetcher

H = {"content-type": "text/html"}
SEED = "https://news.example.com/"


def test_extract_links_normalizes():
    links = dict(extract_links(fixture_text("index.html"), SEED))
    assert "https://news.example.com/2026/10/story-two" in links  # fragment dropped
    assert "https://other.example.org/x" in links


async def test_discover_respects_domain_and_pattern():
    f = make_fetcher({SEED: (200, H, fixture_text("index.html"))})
    res = await discover(f, SEED, ["example.com"], [r"/2026/"], max_pages=1, max_depth=1, max_duration_s=10)
    urls = {l.url for l in res.links}
    assert urls == {"https://news.example.com/2026/10/story-one", "https://news.example.com/2026/10/story-two"}


async def test_discover_domain_allowlist_excludes_offsite():
    f = make_fetcher({SEED: (200, H, fixture_text("index.html"))})
    res = await discover(f, SEED, ["news.example.com"], [], max_pages=1, max_depth=0, max_duration_s=10)
    assert all("other.example.org" not in l.url for l in res.links)


async def test_discover_page_limit_truncates():
    f = make_fetcher({SEED: (200, H, fixture_text("index.html"))})
    res = await discover(f, SEED, ["news.example.com"], [], max_pages=1, max_depth=2, max_duration_s=10)
    assert res.pages_fetched == 1 and res.truncated


async def test_discover_blocked_seed_reports_error():
    f = make_fetcher({})
    res = await discover(f, "http://127.0.0.1/", [], [], 5, 1, 5)
    assert res.links == [] and "non-public" in res.error
