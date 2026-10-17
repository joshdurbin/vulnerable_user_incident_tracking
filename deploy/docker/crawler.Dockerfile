# syntax=docker/dockerfile:1
FROM python:3.13-slim AS build
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy
COPY crawler/pyproject.toml crawler/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY crawler/src ./src
RUN uv sync --frozen --no-dev

FROM python:3.13-slim
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin crawler
WORKDIR /app
COPY --from=build /app /app
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 CRAWLER_LISTEN=0.0.0.0:9090
USER 10001
EXPOSE 9090
ENTRYPOINT ["collision-crawler"]
