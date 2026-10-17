from __future__ import annotations

import time
import urllib.robotparser

from collections.abc import Awaitable, Callable


class RobotsCache:
    """Per-origin robots.txt cache. Unreachable robots.txt (non-4xx failures) is
    treated as disallow-all for 5 minutes; 404/410 is treated as allow-all."""

    def __init__(self, getter: Callable[[str], Awaitable[tuple[int, str]]], user_agent: str, ttl_s: float):
        self._get = getter
        self._ua = user_agent
        self._ttl = ttl_s
        self._cache: dict[str, tuple[float, urllib.robotparser.RobotFileParser | None]] = {}

    async def allowed(self, scheme: str, host: str, url: str) -> bool:
        origin = f"{scheme}://{host}"
        now = time.monotonic()
        hit = self._cache.get(origin)
        if hit is None or hit[0] < now:
            hit = await self._load(origin, now)
            self._cache[origin] = hit
        parser = hit[1]
        return True if parser is None else parser.can_fetch(self._ua, url)

    async def _load(self, origin: str, now: float):
        parser = urllib.robotparser.RobotFileParser()
        try:
            status, body = await self._get(origin + "/robots.txt")
        except Exception:
            parser.parse(["User-agent: *", "Disallow: /"])
            return (now + 300, parser)
        if status in (404, 410):
            return (now + self._ttl, None)
        if status != 200:
            parser.parse(["User-agent: *", "Disallow: /"])
            return (now + 300, parser)
        parser.parse(body[:512_000].splitlines())
        return (now + self._ttl, parser)
