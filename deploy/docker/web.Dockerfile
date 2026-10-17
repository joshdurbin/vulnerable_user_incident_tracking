# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:22-slim AS build
WORKDIR /app
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM nginxinc/nginx-unprivileged:stable-alpine
ENV API_UPSTREAM=http://api:8080
COPY --from=build /app/dist /usr/share/nginx/html
COPY deploy/web/nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=755 deploy/web/40-config-json.sh /docker-entrypoint.d/40-config-json.sh
USER root
RUN chown -R 101:101 /usr/share/nginx/html
USER 101
EXPOSE 8080
