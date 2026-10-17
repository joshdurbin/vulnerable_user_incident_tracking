"""Zlib payload codec, wire-compatible with internal/payloadcodec in the Go module.

Large payloads (article text) are compressed before they reach Temporal, which
keeps workflow history small and well under the per-payload size limit. Small
payloads pass through untouched so they stay readable in the Temporal UI.
"""

from __future__ import annotations

import zlib
from collections.abc import Iterable

from temporalio.api.common.v1 import Payload
from temporalio.converter import DataConverter, PayloadCodec

ENCODING = b"binary/zlib"
THRESHOLD = 1024  # bytes; below this the overhead is not worth it


class ZlibCodec(PayloadCodec):
    async def encode(self, payloads: Iterable[Payload]) -> list[Payload]:
        return [self._encode_one(p) for p in payloads]

    async def decode(self, payloads: Iterable[Payload]) -> list[Payload]:
        return [self._decode_one(p) for p in payloads]

    @staticmethod
    def _encode_one(p: Payload) -> Payload:
        raw = p.SerializeToString()
        if len(raw) < THRESHOLD:
            return p
        return Payload(metadata={"encoding": ENCODING}, data=zlib.compress(raw))

    @staticmethod
    def _decode_one(p: Payload) -> Payload:
        if p.metadata.get("encoding") != ENCODING:
            return p
        out = Payload()
        out.ParseFromString(zlib.decompress(p.data))
        return out


def data_converter() -> DataConverter:
    import dataclasses

    return dataclasses.replace(DataConverter.default, payload_codec=ZlibCodec())
