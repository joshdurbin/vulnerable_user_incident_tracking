"""gRPC server for CrawlerService."""

from __future__ import annotations

import asyncio
import logging
import signal
import sys

import grpc
from google.protobuf.timestamp_pb2 import Timestamp

from .config import Config
from .fetcher import Fetcher
from .gen.crawler.v1 import crawler_pb2 as pb
from .gen.crawler.v1 import crawler_pb2_grpc as pbg
from .links import discover

log = logging.getLogger("collision_crawler")

_RENDER = {
    pb.RENDERING_POLICY_UNSPECIFIED: "HTTP_ONLY",
    pb.RENDERING_POLICY_HTTP_ONLY: "HTTP_ONLY",
    pb.RENDERING_POLICY_AUTO: "AUTO",
    pb.RENDERING_POLICY_ALWAYS: "ALWAYS",
}
_STATUS = {
    "OK": pb.EXTRACTION_STATUS_OK,
    "NO_CONTENT": pb.EXTRACTION_STATUS_NO_CONTENT,
    "FETCH_FAILED": pb.EXTRACTION_STATUS_FETCH_FAILED,
    "BLOCKED": pb.EXTRACTION_STATUS_BLOCKED,
    "ROBOTS_DISALLOWED": pb.EXTRACTION_STATUS_ROBOTS_DISALLOWED,
    "UNSUPPORTED": pb.EXTRACTION_STATUS_UNSUPPORTED,
}


def _ts(dt) -> Timestamp | None:
    if dt is None:
        return None
    t = Timestamp()
    t.FromDatetime(dt)
    return t


def _secs(d) -> float | None:
    s = d.seconds + d.nanos / 1e9
    return s if s > 0 else None


class CrawlerServicer(pbg.CrawlerServiceServicer):
    def __init__(self, fetcher: Fetcher):
        self._f = fetcher

    async def FetchArticle(self, request: pb.FetchArticleRequest, context):
        if not request.url:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "url is required")
        timeout = _secs(request.timeout) if request.HasField("timeout") else None
        # Never log page content; URL host only is enough for tracing.
        log.info("fetch cid=%s source=%s url=%s", request.correlation_id, request.source_id, request.url)
        art = await self._f.fetch_article(request.url, _RENDER[request.rendering_policy], timeout)
        e, p = art.extracted, art.page
        resp = pb.FetchArticleResponse(
            requested_url=request.url,
            final_url=p.final_url,
            canonical_url=e.canonical_url,
            title=e.title,
            publisher=e.publisher,
            author=e.author,
            text=e.text,
            description=e.description,
            language=e.language,
            http_status=p.status,
            content_type=p.content_type,
            rendered=art.rendered,
            extraction_status=_STATUS[art.extraction_status],
            error_detail=p.error,
        )
        if e.published_at:
            resp.published_at.CopyFrom(_ts(e.published_at))
        resp.fetched_at.CopyFrom(_ts(art.fetched_at))
        return resp

    async def DiscoverLinks(self, request: pb.DiscoverLinksRequest, context):
        if not request.seed_url:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "seed_url is required")
        dur = _secs(request.max_duration) if request.HasField("max_duration") else 60.0
        try:
            res = await discover(self._f, request.seed_url, list(request.allowed_domains),
                                 list(request.url_patterns), request.max_pages, request.max_depth, dur or 60.0)
        except Exception as exc:  # bad regex etc. must not crash the server
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"{type(exc).__name__}: {exc}")
        return pb.DiscoverLinksResponse(
            links=[pb.DiscoveredLink(url=l.url, title=l.title, found_on=l.found_on, depth=l.depth) for l in res.links],
            pages_fetched=res.pages_fetched, truncated=res.truncated, error_detail=res.error,
        )


async def serve(cfg: Config) -> None:
    fetcher = Fetcher(cfg)
    server = grpc.aio.server(options=[("grpc.max_receive_message_length", 4 * 1024 * 1024),
                                      ("grpc.max_send_message_length", 16 * 1024 * 1024)])
    pbg.add_CrawlerServiceServicer_to_server(CrawlerServicer(fetcher), server)
    server.add_insecure_port(cfg.listen)
    await server.start()
    log.info("crawler listening on %s browser=%s", cfg.listen, cfg.browser_enabled)
    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, stop.set)
    await stop.wait()
    await server.stop(grace=10)
    await fetcher.aclose()


def main() -> None:
    logging.basicConfig(level=logging.INFO, stream=sys.stdout,
                        format='{"ts":"%(asctime)s","level":"%(levelname)s","logger":"%(name)s","msg":"%(message)s"}')
    asyncio.run(serve(Config.from_env()))


if __name__ == "__main__":
    main()
