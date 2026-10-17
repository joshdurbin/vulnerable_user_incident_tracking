import json
from pathlib import Path

from temporalio.api.common.v1 import Payload
from temporalio.converter import DataConverter

from collision_crawler.activities import ArticleResponse
from collision_crawler.codec import ENCODING, ZlibCodec, data_converter

INTEROP = Path(__file__).parent / "fixtures" / "codec_interop.json"


async def test_small_payloads_pass_through_readable():
    dc = data_converter()
    payloads = await dc.encode(["hello"])
    assert payloads[0].metadata["encoding"] == b"json/plain"
    assert await dc.decode(payloads, [str]) == ["hello"]


async def test_large_article_is_compressed_and_roundtrips():
    art = ArticleResponse(text="A pedestrian was struck on Colorado Boulevard. " * 2000, title="t")
    dc = data_converter()
    (p,) = await dc.encode([art])
    assert p.metadata["encoding"] == ENCODING
    plain = DataConverter.default
    (raw,) = await plain.encode([art])
    assert len(p.data) < len(raw.data) / 10
    (back,) = await dc.decode([p], [ArticleResponse])
    assert back == art


async def test_payload_decodes_in_go_fixture_format():
    """The fixture is read by the Go codec test (internal/payloadcodec); regenerate with:
    REGEN_CODEC_FIXTURE=1 uv run pytest tests/test_codec.py"""
    import os

    text = "interop payload " * 500
    if os.environ.get("REGEN_CODEC_FIXTURE"):
        (p,) = await ZlibCodec().encode([Payload(metadata={"encoding": b"json/plain"}, data=json.dumps(text).encode())])
        INTEROP.write_text(json.dumps({"payload": p.SerializeToString().hex(), "plain": text}))
    fx = json.loads(INTEROP.read_text())
    p = Payload()
    p.ParseFromString(bytes.fromhex(fx["payload"]))
    (out,) = await ZlibCodec().decode([p])
    assert json.loads(out.data) == fx["plain"]
