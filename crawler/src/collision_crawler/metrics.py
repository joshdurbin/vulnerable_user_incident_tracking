"""Application metrics, emitted through the Temporal SDK's meter so they share one
/metrics endpoint (and one naming scheme) with the SDK's own activity metrics.

Labels are bounded sets only (outcome, status class, render result). Never a domain
or URL: those are unbounded and would blow up the time series count.
"""

from __future__ import annotations

from datetime import timedelta

from temporalio.common import MetricMeter

PREFIX = "collision_crawler_"


# Default SDK buckets end at 10s; browser renders and slow sites run well past that.
BUCKETS = {
    PREFIX + "fetch_duration": [0.1, 0.5, 1, 2.5, 5, 10, 20, 40, 80, 160],
    PREFIX + "domain_wait": [0.01, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60],
    PREFIX + "response_size": [1e4, 5e4, 1e5, 5e5, 1e6, 2e6, 5e6, 1e7],
}


def status_class(status: int) -> str:
    return f"{status // 100}xx" if 100 <= status < 600 else "none"


class Metrics:
    def __init__(self, meter: MetricMeter | None = None):
        m = meter or MetricMeter.noop
        d = lambda n, desc: m.create_counter(PREFIX + n, desc)  # noqa: E731
        self._fetch = d("fetch", "Article fetches by final outcome (OK, NO_CONTENT, BLOCKED, ...)")
        self._http = d("http_responses", "Final HTTP responses by status class")
        self._render = d("render", "Browser rendering attempts by result")
        self._pages = d("discover_pages", "Pages fetched while discovering links")
        self._links = d("discover_links", "Links found while discovering")
        self._truncated = d("discover_truncated", "Link discoveries that hit a page or time limit")
        self._fetch_s = m.create_histogram_timedelta(PREFIX + "fetch_duration", "End-to-end article fetch time")
        self._wait_s = m.create_histogram_timedelta(PREFIX + "domain_wait", "Time spent waiting on the per-domain politeness limiter")
        self._bytes = m.create_histogram(PREFIX + "response_size", "Response body size", "bytes")

    def fetch(self, outcome: str, rendered: bool, seconds: float) -> None:
        self._fetch.add(1, {"outcome": outcome, "rendered": str(rendered).lower()})
        self._fetch_s.record(timedelta(seconds=seconds), {"outcome": outcome})

    def http(self, status: int) -> None:
        self._http.add(1, {"class": status_class(status)})

    def body_bytes(self, n: int) -> None:
        self._bytes.record(n)

    def domain_wait(self, seconds: float) -> None:
        self._wait_s.record(timedelta(seconds=seconds))

    def render(self, result: str) -> None:
        self._render.add(1, {"result": result})

    def discovery(self, pages: int, links: int, truncated: bool) -> None:
        self._pages.add(pages)
        self._links.add(links)
        if truncated:
            self._truncated.add(1)
