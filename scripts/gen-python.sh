#!/usr/bin/env bash
# Generate Python gRPC stubs into crawler/src/collision_crawler/gen.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/crawler/src/collision_crawler/gen"
rm -rf "$out" && mkdir -p "$out"
(cd "$root/crawler" && uv run python -m grpc_tools.protoc \
  -I "$root/proto" --python_out="$out" --grpc_python_out="$out" \
  "$root/proto/crawler/v1/crawler.proto")
# protoc emits absolute `crawler.v1` imports; rewrite to the package-relative path.
sed -i.bak 's/^from crawler\.v1 import/from collision_crawler.gen.crawler.v1 import/' "$out/crawler/v1/crawler_pb2_grpc.py"
rm -f "$out"/crawler/v1/*.bak
find "$out" -type d -exec touch {}/__init__.py \;
