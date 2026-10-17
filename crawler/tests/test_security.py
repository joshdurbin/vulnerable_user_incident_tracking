import asyncio
import ipaddress
import socket

import pytest

from collision_crawler import security
from collision_crawler.security import BlockedError, ip_is_public, resolve_public, validate_url


@pytest.mark.parametrize("url", [
    "file:///etc/passwd", "ftp://example.com/x", "gopher://example.com", "javascript:alert(1)",
    "http://127.0.0.1/", "http://localhost/", "http://10.0.0.5/", "http://192.168.1.1/",
    "http://169.254.169.254/latest/meta-data", "http://[::1]/", "http://[::ffff:127.0.0.1]/",
    "http://0.0.0.0/", "http://100.64.0.1/", "http://redis.redis.svc.cluster.local/",
    "http://temporal-frontend.temporal.svc/", "http://example.com:22/", "http://user:pw@example.com/",
    "http:///nohost", "http://a.internal/",
])
def test_blocked_urls(url):
    with pytest.raises(BlockedError):
        validate_url(url)


@pytest.mark.parametrize("url", ["https://news.example.com/a?b=1", "http://example.com:8080/x", "https://8.8.8.8/"])
def test_allowed_urls(url):
    assert validate_url(url).url == url


def test_domain_allow_list():
    validate_url("https://news.example.com/x", ["example.com"])
    with pytest.raises(BlockedError):
        validate_url("https://evilexample.com/x", ["example.com"])
    with pytest.raises(BlockedError):
        validate_url("https://example.com.evil.net/x", ["example.com"])


def test_ip_is_public():
    assert ip_is_public(ipaddress.ip_address("8.8.8.8"))
    for bad in ["127.0.0.1", "10.1.2.3", "172.16.0.1", "169.254.1.1", "fd00::1", "::1", "224.0.0.1", "::ffff:10.0.0.1"]:
        assert not ip_is_public(ipaddress.ip_address(bad)), bad


async def test_resolve_rejects_any_private_answer(monkeypatch):
    """A name with one public and one private A record must be rejected (rebinding mix)."""
    async def fake(self, host, port, **kw):
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("8.8.8.8", port)),
                (socket.AF_INET, socket.SOCK_STREAM, 6, "", ("10.0.0.1", port))]
    monkeypatch.setattr(asyncio.get_event_loop_policy().__class__, "_x", None, raising=False)
    loop = asyncio.get_running_loop()
    monkeypatch.setattr(type(loop), "getaddrinfo", fake)
    with pytest.raises(BlockedError):
        await resolve_public("mixed.example.com", 443)


async def test_resolve_returns_validated_ips(monkeypatch):
    async def fake(self, host, port, **kw):
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("93.184.216.34", port))]
    monkeypatch.setattr(type(asyncio.get_running_loop()), "getaddrinfo", fake)
    assert await resolve_public("example.com", 443) == ["93.184.216.34"]
