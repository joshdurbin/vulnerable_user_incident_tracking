import pytest
from temporalio.exceptions import ApplicationError
from temporalio.testing import ActivityEnvironment

from collision_crawler.activities import (
    CrawlerActivities, DiscoverLinksRequest, FetchArticleRequest,
)

from .conftest import fixture_text, make_fetcher

ART = "https://news.example.com/a"


@pytest.fixture
def acts():
    f = make_fetcher({
        ART: (200, {"content-type": "text/html"}, fixture_text("pedestrian_fatality.html")),
        "https://news.example.com/": (200, {"content-type": "text/html"}, fixture_text("index.html")),
    })
    return CrawlerActivities(f)


async def test_fetch_article_returns_extracted_fields(acts):
    r = await ActivityEnvironment().run(acts.fetch_article, FetchArticleRequest(url=ART, source_id="rss:test", correlation_id="c1"))
    assert r.extraction_status == "OK"
    assert r.requested_url == ART and r.final_url == ART and r.http_status == 200
    assert r.publisher == "Example Gazette" and r.language == "en"
    assert r.published_at and r.fetched_at
    assert "pronounced dead" in r.text


async def test_blocked_url_is_a_status_not_an_error(acts):
    r = await ActivityEnvironment().run(acts.fetch_article, FetchArticleRequest(url="http://169.254.169.254/"))
    assert r.extraction_status == "BLOCKED" and r.error_detail


async def test_missing_url_is_non_retryable(acts):
    with pytest.raises(ApplicationError) as e:
        await ActivityEnvironment().run(acts.fetch_article, FetchArticleRequest())
    assert e.value.non_retryable


async def test_invalid_rendering_is_non_retryable(acts):
    with pytest.raises(ApplicationError) as e:
        await ActivityEnvironment().run(acts.fetch_article, FetchArticleRequest(url=ART, rendering="bogus"))
    assert e.value.non_retryable


async def test_discover_links(acts):
    r = await ActivityEnvironment().run(acts.discover_links, DiscoverLinksRequest(
        seed_url="https://news.example.com/", allowed_domains=["example.com"], url_patterns=["/2026/"], max_pages=1, max_depth=1))
    assert {l.url for l in r.links} == {"https://news.example.com/2026/10/story-one", "https://news.example.com/2026/10/story-two"}


async def test_bad_pattern_is_non_retryable(acts):
    with pytest.raises(ApplicationError) as e:
        await ActivityEnvironment().run(acts.discover_links, DiscoverLinksRequest(
            seed_url="https://news.example.com/", url_patterns=["("], max_pages=1))
    assert e.value.non_retryable
