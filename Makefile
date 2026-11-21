SHELL := /bin/bash
ENV   := set -a; [ -f .env ] && . ./.env; set +a;
TEST_DB ?= postgres://pms_app:pms_local_password@localhost:5432/pms_test?sslmode=disable

.PHONY: help setup run build test test-unit test-integration vet fmt migrate-up migrate-down reconcile \
        deps-up deps-down up down smoke

help:            ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/'

setup:           ## create .env and download dependencies (generates go.sum)
	@[ -f .env ] || (cp .env.example .env && echo "created .env")
	go mod tidy

run:             ## run the API on the host using .env
	@$(ENV) go run ./cmd/api

build:           ## build binaries into ./bin
	CGO_ENABLED=0 go build -o bin/ ./cmd/api ./cmd/migrate ./cmd/reconcile

vet:
	go vet ./...

fmt:
	gofmt -l -w .

test-unit:       ## unit tests (no database needed)
	go test ./internal/...

test-integration: ## API tests against PostgreSQL (DROPS the public schema of TEST_DB)
	TEST_DATABASE_URL='$(TEST_DB)' go test -race -count=1 ./tests/integration/...

test: vet test-unit test-integration

migrate-up:      ## apply pending migrations
	@$(ENV) go run ./cmd/migrate up

migrate-down:    ## revert the last migration (N=2 to revert two)
	@$(ENV) go run ./cmd/migrate down $(or $(N),1)

reconcile:       ## report orphaned image objects (add ARGS=--delete to remove them)
	@$(ENV) go run ./cmd/reconcile $(ARGS)

deps-up:         ## start only PostgreSQL in Docker
	docker compose -f docker-compose.deps.yml up -d
	@echo "creating test database (ignore 'already exists')"; sleep 3; \
	docker compose -f docker-compose.deps.yml exec -T db psql -U pms_app -d postgres -c 'CREATE DATABASE pms_test' || true

deps-down:
	docker compose -f docker-compose.deps.yml down

up:              ## full stack: PostgreSQL + MinIO + API
	docker compose up --build -d
	@echo "API: http://localhost:8080  docs: http://localhost:8080/docs  MinIO console: http://localhost:9001"

down:
	docker compose down

smoke:           ## end-to-end check against a running API (BASE_URL, ADMIN_USER, ADMIN_PASS)
	./scripts/smoke.sh
