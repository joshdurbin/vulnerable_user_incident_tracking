from __future__ import annotations

import os
from dataclasses import dataclass


@dataclass(frozen=True)
class Config:
    temporal_address: str = "temporal-frontend.temporal.svc.cluster.local:7233"
    temporal_namespace: str = "collisions"
    task_queue: str = "collision-crawl"
    user_agent: str = "CollisionTrackerBot/0.1 (+https://github.com/joshdurbin/fuck_cars)"
    max_response_bytes: int = 5 * 1024 * 1024
    default_timeout_s: float = 20.0
    max_redirects: int = 5
    per_domain_min_interval_s: float = 2.0
    robots_ttl_s: float = 3600.0
    browser_enabled: bool = False
    browser_concurrency: int = 1
    max_concurrent_fetches: int = 8

    @classmethod
    def from_env(cls) -> "Config":
        e = os.environ.get
        return cls(
            temporal_address=e("CRAWLER_TEMPORAL_ADDRESS", cls.temporal_address),
            temporal_namespace=e("CRAWLER_TEMPORAL_NAMESPACE", cls.temporal_namespace),
            task_queue=e("CRAWLER_TASK_QUEUE", cls.task_queue),
            user_agent=e("CRAWLER_USER_AGENT", cls.user_agent),
            max_response_bytes=int(e("CRAWLER_MAX_RESPONSE_BYTES", cls.max_response_bytes)),
            default_timeout_s=float(e("CRAWLER_DEFAULT_TIMEOUT_S", cls.default_timeout_s)),
            per_domain_min_interval_s=float(e("CRAWLER_PER_DOMAIN_INTERVAL_S", cls.per_domain_min_interval_s)),
            browser_enabled=e("CRAWLER_BROWSER_ENABLED", "false").lower() == "true",
            browser_concurrency=int(e("CRAWLER_BROWSER_CONCURRENCY", cls.browser_concurrency)),
            max_concurrent_fetches=int(e("CRAWLER_MAX_CONCURRENT_FETCHES", cls.max_concurrent_fetches)),
        )
