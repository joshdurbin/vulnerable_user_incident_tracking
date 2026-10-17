"""URL and address validation. Every URL and redirect target is untrusted."""

from __future__ import annotations

import asyncio
import ipaddress
import socket
from dataclasses import dataclass
from urllib.parse import urlsplit


class BlockedError(Exception):
    """Raised when a URL or address violates the security policy."""


ALLOWED_SCHEMES = {"http", "https"}
ALLOWED_PORTS = {80, 443, 8080, 8443}


def ip_is_public(ip: ipaddress.IPv4Address | ipaddress.IPv6Address) -> bool:
    if isinstance(ip, ipaddress.IPv6Address) and ip.ipv4_mapped:
        ip = ip.ipv4_mapped
    return ip.is_global and not (
        ip.is_multicast or ip.is_loopback or ip.is_link_local or ip.is_private
    )


@dataclass(frozen=True)
class ParsedURL:
    url: str
    scheme: str
    host: str
    port: int


def validate_url(url: str, allowed_domains: list[str] | None = None) -> ParsedURL:
    """Validate scheme, host, port and optional domain allow-list (no DNS)."""
    if len(url) > 2048:
        raise BlockedError("url too long")
    parts = urlsplit(url)
    if parts.scheme.lower() not in ALLOWED_SCHEMES:
        raise BlockedError(f"scheme not allowed: {parts.scheme!r}")
    if parts.username or parts.password:
        raise BlockedError("credentials in url not allowed")
    host = (parts.hostname or "").lower().rstrip(".")
    if not host:
        raise BlockedError("missing host")
    try:
        port = parts.port or (443 if parts.scheme.lower() == "https" else 80)
    except ValueError as exc:
        raise BlockedError("invalid port") from exc
    if port not in ALLOWED_PORTS:
        raise BlockedError(f"port not allowed: {port}")
    try:
        literal = ipaddress.ip_address(host)
    except ValueError:
        literal = None
        if host == "localhost" or host.endswith((".local", ".internal", ".localhost", ".svc", ".cluster.local")):
            raise BlockedError(f"internal hostname not allowed: {host}")
    if literal is not None and not ip_is_public(literal):
        raise BlockedError(f"non-public address not allowed: {host}")
    if allowed_domains and not any(host == d or host.endswith("." + d) for d in (x.lower() for x in allowed_domains)):
        raise BlockedError(f"domain not in allow-list: {host}")
    return ParsedURL(url=url, scheme=parts.scheme.lower(), host=host, port=port)


async def resolve_public(host: str, port: int) -> list[str]:
    """Resolve host and return only if EVERY address is public.

    Returning the validated addresses lets the caller connect to exactly the
    IP that was checked, which closes the DNS-rebinding window.
    """
    try:
        literal = ipaddress.ip_address(host)
        addrs = [str(literal)]
    except ValueError:
        loop = asyncio.get_running_loop()
        try:
            infos = await loop.getaddrinfo(host, port, type=socket.SOCK_STREAM)
        except socket.gaierror as exc:
            raise BlockedError(f"dns resolution failed for {host}") from exc
        addrs = list(dict.fromkeys(info[4][0] for info in infos))
    if not addrs:
        raise BlockedError(f"no addresses for {host}")
    for a in addrs:
        if not ip_is_public(ipaddress.ip_address(a)):
            raise BlockedError(f"{host} resolves to non-public address {a}")
    return addrs
