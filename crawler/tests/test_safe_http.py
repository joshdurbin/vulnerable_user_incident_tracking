"""Exercise the real pinned network backend against a local socket server."""
import asyncio

import httpx
import pytest

from collision_crawler.safe_http import make_client
from collision_crawler.security import BlockedError


@pytest.fixture
async def local_server():
    async def handle(reader, writer):
        await reader.readuntil(b"\r\n\r\n")
        writer.write(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nContent-Type: text/html\r\n\r\nhi")
        await writer.drain()
        writer.close()
    srv = await asyncio.start_server(handle, "127.0.0.1", 0)
    yield srv.sockets[0].getsockname()[1]
    srv.close()
    await srv.wait_closed()


async def test_connect_to_loopback_is_refused_even_if_url_check_bypassed(local_server):
    async with make_client("t") as c:
        with pytest.raises((BlockedError, httpx.HTTPError)):
            await c.get(f"http://127.0.0.1:{local_server}/")


async def test_hostname_resolving_to_loopback_is_refused(local_server):
    # 'localhost' resolves to 127.0.0.1/::1 -> must be rejected at connect time
    async with make_client("t") as c:
        with pytest.raises((BlockedError, httpx.HTTPError)):
            await c.get(f"http://localhost:{local_server}/")
