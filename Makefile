# SoğukZincir — common commands. Everything runs in Docker; Go/Node are only
# needed locally for `make test` / `make lint`.

COMPOSE ?= docker compose

.PHONY: up down logs verify load reset-demo chaos test test-integration lint cover env

env:
	@test -f .env || cp .env.example .env

## up: build and start everything (frontend :3000, API :8080)
up: env
	$(COMPOSE) up --build -d
	@echo "Arayüz: http://localhost:3000   API: http://localhost:8080   Doküman: http://localhost:8080/docs"

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100 backend simulator

## verify: end-to-end check of every feature and hard task (PASS/FAIL table)
verify: env
	$(COMPOSE) up --build -d --wait backend frontend simulator
	$(COMPOSE) --profile verify run --rm --build verify

## load: k6 load test (results in load/results/summary.json); the simulator
## pauses meanwhile because k6 plays the devices
load: env
	@mkdir -p load/results
	$(COMPOSE) up -d --wait backend
	$(COMPOSE) stop simulator
	$(COMPOSE) --profile load run --rm k6 || true
	$(COMPOSE) start simulator
	@python3 load/summary.py load/results/summary.json

## reset-demo: drop all data and seed again with the same SEED
reset-demo: env
	$(COMPOSE) stop simulator backend
	$(COMPOSE) run --rm migrate reset
	$(COMPOSE) up -d --wait backend simulator

## chaos: stop the database, show the API degrade and recover
chaos:
	./scripts/chaos.sh

## test: unit tests (no database needed) + frontend tests
test:
	cd backend && go test ./internal/domain/... ./internal/auth/... ./internal/httpx/...
	cd frontend && npm test

## test-integration: integration tests against a throwaway PostgreSQL
test-integration:
	./scripts/test-integration.sh

## cover: domain package coverage report
cover:
	cd backend && go test -coverprofile=coverage.out ./internal/domain/... && go tool cover -func=coverage.out | tail -1

lint:
	cd backend && go vet ./... && test -z "$$(gofmt -l .)"
	cd frontend && npm run lint
