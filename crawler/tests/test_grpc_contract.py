import grpc
import pytest

from collision_crawler.gen.crawler.v1 import crawler_pb2 as pb
from collision_crawler.gen.crawler.v1 import crawler_pb2_grpc as pbg
from collision_crawler.server import CrawlerServicer

from .conftest import fixture_text, make_fetcher

ART = "https://news.example.com/a"


@pytest.fixture
async def stub():
    f = make_fetcher({
        ART: (200, {"content-type": "text/html"}, fixture_text("pedestrian_fatality.html")),
        "https://news.example.com/": (200, {"content-type": "text/html"}, fixture_text("index.html")),
    })
    server = grpc.aio.server()
    pbg.add_CrawlerServiceServicer_to_server(CrawlerServicer(f), server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    async with grpc.aio.insecure_channel(f"127.0.0.1:{port}") as ch:
        yield pbg.CrawlerServiceStub(ch)
    await server.stop(None)


async def test_fetch_article_contract(stub):
    r = await stub.FetchArticle(pb.FetchArticleRequest(url=ART, source_id="rss:test", correlation_id="c1"))
    assert r.extraction_status == pb.EXTRACTION_STATUS_OK
    assert r.requested_url == ART and r.final_url == ART and r.http_status == 200
    assert r.publisher == "Example Gazette" and r.language == "en"
    assert r.published_at.seconds > 0 and r.fetched_at.seconds > 0
    assert "pronounced dead" in r.text


async def test_fetch_blocked_url_returns_status_not_error(stub):
    r = await stub.FetchArticle(pb.FetchArticleRequest(url="http://169.254.169.254/"))
    assert r.extraction_status == pb.EXTRACTION_STATUS_BLOCKED and r.error_detail


async def test_fetch_requires_url(stub):
    with pytest.raises(grpc.aio.AioRpcError) as e:
        await stub.FetchArticle(pb.FetchArticleRequest())
    assert e.value.code() == grpc.StatusCode.INVALID_ARGUMENT


async def test_discover_links_contract(stub):
    r = await stub.DiscoverLinks(pb.DiscoverLinksRequest(
        seed_url="https://news.example.com/", allowed_domains=["example.com"], url_patterns=["/2026/"],
        max_pages=1, max_depth=1))
    assert {l.url for l in r.links} == {"https://news.example.com/2026/10/story-one", "https://news.example.com/2026/10/story-two"}
