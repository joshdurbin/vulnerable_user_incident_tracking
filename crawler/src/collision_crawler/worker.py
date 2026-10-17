"""Temporal worker for the crawl task queue."""

from __future__ import annotations

import asyncio
import logging
import sys

from temporalio.client import Client
from temporalio.runtime import PrometheusConfig, Runtime, TelemetryConfig
from temporalio.worker import Worker

from .activities import CrawlerActivities
from .codec import data_converter
from .config import Config
from .fetcher import Fetcher

log = logging.getLogger("collision_crawler")


async def run(cfg: Config) -> None:
    # SDK metrics (activity latency, failures, task slots) on /metrics, scraped by VictoriaMetrics.
    runtime = Runtime(telemetry=TelemetryConfig(metrics=PrometheusConfig(bind_address=cfg.metrics_address)))
    client = await Client.connect(
        cfg.temporal_address, namespace=cfg.temporal_namespace, data_converter=data_converter(), runtime=runtime
    )
    fetcher = Fetcher(cfg)
    acts = CrawlerActivities(fetcher)
    worker = Worker(
        client, task_queue=cfg.task_queue,
        activities=[acts.fetch_article, acts.discover_links],
        max_concurrent_activities=cfg.max_concurrent_fetches,
    )
    log.info("crawler worker ready address=%s namespace=%s queue=%s browser=%s metrics=%s",
             cfg.temporal_address, cfg.temporal_namespace, cfg.task_queue, cfg.browser_enabled, cfg.metrics_address)
    try:
        await worker.run()  # returns on SIGINT/SIGTERM-driven cancellation
    finally:
        await fetcher.aclose()


def main() -> None:
    logging.basicConfig(level=logging.INFO, stream=sys.stdout,
                        format='{"ts":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":"%(message)s"}')
    try:
        asyncio.run(run(Config.from_env()))
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
