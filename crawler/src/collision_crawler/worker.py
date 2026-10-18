"""Temporal worker for the crawl task queue."""

from __future__ import annotations

import asyncio
import logging
import sys

from temporalio.client import Client
from temporalio.runtime import PrometheusConfig, Runtime, TelemetryConfig
from temporalio.worker import Worker

from .activities import CrawlerActivities, RenderActivities
from .codec import data_converter
from .config import Config
from .fetcher import Fetcher
from .metrics import BUCKETS, Metrics

log = logging.getLogger("collision_crawler")


async def run(cfg: Config) -> None:
    # SDK metrics (activity latency, failures, task slots) on /metrics, scraped by VictoriaMetrics.
    runtime = Runtime(telemetry=TelemetryConfig(metrics=PrometheusConfig(
        bind_address=cfg.metrics_address,
        # Same naming as the Go workers (…_total, …_seconds) so one dashboard and one set of alerts cover both.
        counters_total_suffix=True, unit_suffix=True, durations_as_seconds=True,
        histogram_bucket_overrides=BUCKETS,
    )))
    client = await Client.connect(
        cfg.temporal_address, namespace=cfg.temporal_namespace, data_converter=data_converter(), runtime=runtime
    )
    metrics = Metrics(runtime.metric_meter)
    if cfg.mode == "render":
        from .renderer import Renderer  # needs the browser extra, only present in the render image

        renderer = Renderer(cfg, metrics)
        activities, closer = [RenderActivities(renderer).render_article], renderer.aclose
        slots = cfg.browser_concurrency
    elif cfg.mode == "crawl":
        fetcher = Fetcher(cfg, metrics=metrics)
        acts = CrawlerActivities(fetcher)
        activities, closer = [acts.fetch_article, acts.discover_links], fetcher.aclose
        slots = cfg.max_concurrent_fetches
    else:
        raise SystemExit(f"CRAWLER_MODE must be crawl or render, got {cfg.mode!r}")
    worker = Worker(client, task_queue=cfg.queue, activities=activities, max_concurrent_activities=slots)
    log.info("crawler worker ready mode=%s address=%s namespace=%s queue=%s slots=%d metrics=%s",
             cfg.mode, cfg.temporal_address, cfg.temporal_namespace, cfg.queue, slots, cfg.metrics_address)
    try:
        await worker.run()  # returns on SIGINT/SIGTERM-driven cancellation
    finally:
        await closer()


def main() -> None:
    logging.basicConfig(level=logging.INFO, stream=sys.stdout,
                        format='{"ts":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":"%(message)s"}')
    try:
        asyncio.run(run(Config.from_env()))
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
