SHELL := /bin/bash
GOBIN := $(shell go env GOPATH)/bin
export PATH := $(PATH):$(GOBIN)
IMAGE_PREFIX ?= ghcr.io/joshdurbin/vulnerable_user_incident_tracking
TAG ?= dev

.PHONY: helm-check test-web web-dev generate eval eval-replay lint test test-go test-py test-integration build docker-build dev-up dev-down migrate

generate: ## regenerate sqlc queries and the API server from api/openapi.yaml
	sqlc generate
	cd web && npm run gen:api
	cd internal/api && go tool oapi-codegen -config oapi.cfg.yaml ../../api/openapi.yaml

lint:
	go vet ./...
	gofmt -l . | (! grep .)

test: test-go test-py test-web

test-go:
	go test ./...

helm-check: ## lint the chart, render it, and validate against Kubernetes schemas
	helm lint deploy/helm/collision-tracker --set llm.model=x
	helm template ct deploy/helm/collision-tracker --set llm.model=x | go run github.com/yannh/kubeconform/cmd/kubeconform@latest -strict -summary -ignore-missing-schemas -kubernetes-version 1.33.0
	helm template ct deploy/helm/collision-tracker --set llm.model=x --set render.enabled=true | go run github.com/yannh/kubeconform/cmd/kubeconform@latest -strict -summary -ignore-missing-schemas -kubernetes-version 1.33.0

test-web:
	cd web && npm test

test-py:
	cd crawler && uv run pytest -q

eval-replay: ## harness check only; scores are NOT model quality
	go run ./cmd/collisionctl eval --replay

eval: ## score a real model: COLLISION_LLM_MODEL=... COLLISION_LLM_BASE_URL=... make eval
	go run ./cmd/collisionctl eval

test-integration: ## needs docker (postgres, temporal); added in Milestone 3
	TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration -count=1 ./internal/...

build:
	go build -o bin/ ./cmd/...

docker-build: ## local single-arch images; CI builds multi-arch
	docker build -f deploy/docker/crawler.Dockerfile -t $(IMAGE_PREFIX)/crawler:$(TAG) .
	docker build -f deploy/docker/web.Dockerfile -t $(IMAGE_PREFIX)/web:$(TAG) .
	docker build -f deploy/docker/go.Dockerfile --build-arg CMD=collisionctl -t $(IMAGE_PREFIX)/collisionctl:$(TAG) .

dev-up:
	docker compose -f deploy/local/docker-compose.yaml up -d --build

dev-down:
	docker compose -f deploy/local/docker-compose.yaml down

migrate: ## COLLISION_DATABASE_URL=postgres://... make migrate
	go run ./cmd/collisionctl migrate
