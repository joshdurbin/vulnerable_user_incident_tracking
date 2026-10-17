from __future__ import annotations

import asyncio
import time


class DomainLimiter:
    """Enforces a minimum interval between requests to the same host."""

    def __init__(self, min_interval_s: float):
        self._interval = min_interval_s
        self._next: dict[str, float] = {}
        self._lock = asyncio.Lock()

    async def wait(self, host: str) -> None:
        async with self._lock:
            now = time.monotonic()
            at = max(now, self._next.get(host, 0.0))
            self._next[host] = at + self._interval
        delay = at - now
        if delay > 0:
            await asyncio.sleep(delay)
