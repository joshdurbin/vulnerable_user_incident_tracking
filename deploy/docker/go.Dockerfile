# syntax=docker/dockerfile:1
# Builds one Go binary (ARG CMD) for the build platform's target arch.
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG CMD
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
# The starter list of sources. The API loads it only into a database that has none (see COLLISION_SOURCES_SEED_FILE).
COPY --from=build /src/docs/examples/sources.yaml /etc/collision/sources.yaml
USER 65532:65532
ENTRYPOINT ["/app"]
