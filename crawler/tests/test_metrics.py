"""Metrics go through a real Temporal Runtime and are read back from its Prometheus endpoint."""

import socket
import urllib.request

import httpx
import pytest
from temporalio.runtime import PrometheusConfig, Runtime, TelemetryConfig

from collision_crawler.activities import CrawlerActivities, FetchArticleRequest
from collision_crawler.config import Config
from collision_crawler.fetcher import Fetcher
from collision_crawler.metrics import BUCKETS, Metrics, status_class

ARTICLE = "<html><head><title>t</title></head><body><article>" + ("A pedestrian was struck by a car downtown. " * 20) + "</article></body></html>"


def sample(text: str, name: str, **labels: str) -> float:
    """Value of the one series whose name matches and whose labels include `labels`."""
    hits = [line for line in text.splitlines()
            if line.startswith(name + "{") and all(f'{k}="{v}"' in line for k, v in labels.items())]
    assert len(hits) == 1, f"{name}{labels}: {len(hits)} series in\n{text}"
    return float(hits[0].rsplit(" ", 1)[1])


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@pytest.fixture
def scrape():
    addr = f"127.0.0.1:{free_port()}"
    rt = Runtime(telemetry=TelemetryConfig(metrics=PrometheusConfig(
        bind_address=addr, counters_total_suffix=True, unit_suffix=True, durations_as_seconds=True, histogram_bucket_overrides=BUCKETS)))
    return rt, lambda: urllib.request.urlopen(f"http://{addr}/metrics", timeout=5).read().decode()


def fetcher(rt, routes):
    def handler(req):
        r = routes.get(str(req.url))
        return httpx.Response(r[0], headers=r[1], content=r[2]) if r else httpx.Response(404)
    client = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return Fetcher(Config(per_domain_min_interval_s=0.0), client=client, metrics=Metrics(rt.metric_meter))


def test_status_class():
    assert [status_class(s) for s in (0, 200, 301, 404, 503, 999)] == ["none", "2xx", "3xx", "4xx", "5xx", "none"]


@pytest.mark.asyncio
async def test_fetch_outcomes_are_counted_with_bounded_labels(scrape):
    rt, get = scrape
    html = {"content-type": "text/html"}
    f = fetcher(rt, {"https://news.example.com/ok": (200, html, ARTICLE), "https://news.example.com/gone": (404, html, "nope")})
    acts = CrawlerActivities(f)
    for path in ("ok", "ok", "gone"):
        await acts.fetch_article(FetchArticleRequest(url=f"https://news.example.com/{path}"))
    await acts.fetch_article(FetchArticleRequest(url="http://127.0.0.1/admin"))  # blocked before any request

    text = get()
    assert sample(text, "collision_crawler_fetch_total", outcome="OK", rendered="false") == 2
    assert sample(text, "collision_crawler_fetch_total", outcome="FETCH_FAILED") == 1
    assert sample(text, "collision_crawler_fetch_total", outcome="BLOCKED") == 1
    assert sample(text, "collision_crawler_http_responses_total", **{"class": "2xx"}) == 2
    assert sample(text, "collision_crawler_http_responses_total", **{"class": "4xx"}) == 1
    assert sample(text, "collision_crawler_response_size_bytes_count") == 3  # two articles and the 404 page; robots.txt is not counted
    assert sample(text, "collision_crawler_fetch_duration_seconds_count", outcome="OK") == 2
    assert 'fetch_duration_seconds_bucket{outcome="OK",service_name="temporal-core-sdk",le="160"}' in text  # widened buckets applied
    assert "news.example.com" not in text  # no domain or URL ever becomes a label
