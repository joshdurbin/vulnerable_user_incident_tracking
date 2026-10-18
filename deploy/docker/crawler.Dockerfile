# syntax=docker/dockerfile:1
FROM python:3.13-slim AS build
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy
COPY crawler/pyproject.toml crawler/uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY crawler/src ./src
RUN uv sync --frozen --no-dev

# Render image: the same worker plus Chromium. Build with --target render. Much larger than
# the crawl image, which is why it runs as its own Deployment on its own task queue.
FROM python:3.13-slim AS render
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin crawler
WORKDIR /app
COPY --from=build /app /app
COPY crawler/pyproject.toml crawler/uv.lock ./
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
RUN UV_LINK_MODE=copy uv sync --frozen --no-dev --extra browser \
 && PLAYWRIGHT_BROWSERS_PATH=/ms-playwright /app/.venv/bin/playwright install --with-deps --only-shell chromium \
 && rm -rf /var/lib/apt/lists/* /root/.cache
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 PLAYWRIGHT_BROWSERS_PATH=/ms-playwright CRAWLER_MODE=render HOME=/tmp
USER 10001
ENTRYPOINT ["collision-crawler"]

# Default target: plain HTTP crawler (no browser).
FROM python:3.13-slim AS crawl
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin crawler
WORKDIR /app
COPY --from=build /app /app
ENV PATH="/app/.venv/bin:$PATH" PYTHONUNBUFFERED=1 
USER 10001
ENTRYPOINT ["collision-crawler"]
