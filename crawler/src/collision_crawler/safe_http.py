"""HTTP client that only ever connects to validated public IPs."""

from __future__ import annotations

import httpcore
import httpx
from httpcore._backends.anyio import AnyIOBackend

from .security import BlockedError, resolve_public, validate_url


class _PinnedBackend(AnyIOBackend):
    """Resolves and validates at connect time, then dials the checked IP.

    TLS SNI and certificate verification still use the original hostname
    (httpcore passes it separately to start_tls).
    """

    async def connect_tcp(self, host, port, timeout=None, local_address=None, socket_options=None):
        addrs = await resolve_public(host, port)
        last: Exception | None = None
        for addr in addrs:
            try:
                return await super().connect_tcp(addr, port, timeout=timeout, local_address=local_address, socket_options=socket_options)
            except Exception as exc:  # try next address
                last = exc
        assert last is not None
        raise last


class _SafeTransport(httpx.AsyncHTTPTransport):
    def __init__(self, backend: httpcore.AsyncNetworkBackend | None = None, **kwargs):
        super().__init__(**kwargs)
        self._pool = httpcore.AsyncConnectionPool(
            ssl_context=self._pool._ssl_context,  # keep httpx's verified context
            max_connections=20,
            max_keepalive_connections=5,
            keepalive_expiry=5.0,
            http2=False,
            network_backend=backend or _PinnedBackend(),
        )


def make_client(user_agent: str, timeout: float = 15.0, backend=None) -> httpx.AsyncClient:
    return httpx.AsyncClient(
        transport=_SafeTransport(backend=backend),
        headers={"User-Agent": user_agent, "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.5"},
        timeout=httpx.Timeout(timeout),
        follow_redirects=False,  # redirects are validated hop by hop in fetcher
    )


__all__ = ["make_client", "BlockedError", "validate_url"]
