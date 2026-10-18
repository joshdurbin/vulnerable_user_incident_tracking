"""Headless-browser rendering for pages whose text is built by JavaScript.

One Chromium process per worker, shared by every render; each render gets its own
throwaway context (no cookies or storage carry over) and a hard time limit. A
semaphore bounds concurrent pages, because each costs hundreds of MB.

Every request goes through a route handler that validates it and issues it with
redirects disabled (see `guard`).

The browser does its own DNS, so a hostname that passes our check could still
resolve differently when Chromium connects. The pod's egress NetworkPolicy
(private ranges denied) is the backstop for that; the checks here are the first layer.
"""

from __future__ import annotations

import asyncio
import html
import logging
from urllib.parse import urljoin
from dataclasses import dataclass

from playwright.async_api import Browser, Playwright, Route, async_playwright

from .config import Config
from .metrics import Metrics
from .ratelimit import DomainLimiter
from .security import BlockedError, resolve_public, validate_url

log = logging.getLogger(__name__)

# Text comes from the DOM; images, media and fonts only cost memory and time.
_SKIP_RESOURCES = {"image", "media", "font"}
_SETTLE_S = 5.0  # how long to wait for the network to go quiet after DOMContentLoaded


@dataclass
class Rendered:
    html: str
    final_url: str
    status: int


class Renderer:
    def __init__(self, cfg: Config, metrics: Metrics | None = None):
        self.cfg = cfg
        self.metrics = metrics or Metrics()
        self._sem = asyncio.Semaphore(max(1, cfg.browser_concurrency))
        self._limiter = DomainLimiter(cfg.per_domain_min_interval_s)
        self._lock = asyncio.Lock()
        self._pw: Playwright | None = None
        self._browser: Browser | None = None

    async def aclose(self) -> None:
        async with self._lock:
            if self._browser:
                await self._browser.close()
            if self._pw:
                await self._pw.stop()
            self._browser = self._pw = None

    async def _get_browser(self) -> Browser:
        async with self._lock:
            if self._browser is not None and self._browser.is_connected():
                return self._browser
            if self._pw is None:
                self._pw = await async_playwright().start()
            if self._browser is not None:
                self.metrics.render("browser_restart")
                log.warning("chromium disconnected; relaunching")
            # --disable-dev-shm-usage: containers have a tiny /dev/shm.
            self._browser = await self._pw.chromium.launch(args=["--disable-dev-shm-usage"])
            return self._browser

    async def render(self, url: str, timeout_s: float) -> Rendered:
        """Load url in a fresh context and return the rendered DOM.

        Raises BlockedError if the page (or a redirect) violates the URL policy,
        and a plain Exception for browser failures, which Temporal retries.
        """
        async with self._sem:
            return await asyncio.wait_for(self._render(url, timeout_s), timeout=timeout_s + 10)

    async def _render(self, url: str, timeout_s: float) -> Rendered:
        browser = await self._get_browser()
        ctx = await browser.new_context(
            user_agent=self.cfg.user_agent, service_workers="block", accept_downloads=False, java_script_enabled=True,
        )
        blocked: list[str] = []
        nav_final: list[str] = []  # where the main document really ended up after our own redirect handling
        try:
            async def guard(route: Route) -> None:
                req = route.request
                main_nav = req.is_navigation_request() and req.frame.parent_frame is None
                if req.resource_type in _SKIP_RESOURCES or req.method not in ("GET", "HEAD"):
                    return await route.abort()
                # Chromium follows redirects (and even a fulfilled 3xx) without consulting us
                # again, which would let a redirect reach a host we never checked. So we follow
                # them here, validating every hop, and give the browser only the final response.
                url = req.url
                try:
                    for _ in range(self.cfg.max_redirects + 1):
                        parsed = validate_url(url)
                        await resolve_public(parsed.host, parsed.port)
                        if main_nav:
                            await self._limiter.wait(parsed.host)
                        try:
                            resp = await route.fetch(url=url, max_redirects=0, timeout=timeout_s * 1000)
                        except Exception:
                            return await route.abort("failed")
                        loc = resp.headers.get("location")
                        if resp.status in (301, 302, 303, 307, 308) and loc:
                            url = urljoin(url, loc)
                            continue
                        break
                    else:
                        raise BlockedError("too many redirects")
                except BlockedError as exc:
                    if main_nav:
                        blocked.append(str(exc))
                    return await route.abort("blockedbyclient")
                if not main_nav or url == req.url:
                    if main_nav:
                        nav_final.append(url)
                    return await route.fulfill(response=resp)
                # The document came from a different URL than the browser asked for: say so, so
                # relative links resolve against where it really lives.
                nav_final.append(url)
                body = (await resp.body()).decode(resp.headers.get("content-type", "").partition("charset=")[2] or "utf-8", errors="replace")
                await route.fulfill(response=resp, body=f'<base href="{html.escape(url)}">' + body)

            await ctx.route("**/*", guard)
            page = await ctx.new_page()
            try:
                resp = await page.goto(url, wait_until="domcontentloaded", timeout=timeout_s * 1000)
            except Exception:
                if blocked:
                    raise BlockedError(blocked[0]) from None
                raise
            try:  # best effort: late XHRs often carry the article body
                await page.wait_for_load_state("networkidle", timeout=_SETTLE_S * 1000)
            except Exception:
                pass
            doc = await page.content()
            if len(doc.encode()) > self.cfg.max_response_bytes:
                raise BlockedError("rendered page too large")
            return Rendered(html=doc, final_url=nav_final[-1] if nav_final else page.url, status=resp.status if resp else 0)
        finally:
            await ctx.close()
