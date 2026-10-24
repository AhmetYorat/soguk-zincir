#!/usr/bin/env bash
# Runs the integration tests against a throwaway PostgreSQL 16 container.
set -euo pipefail
cd "$(dirname "$0")/../backend"

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  name="sz-it-$$"
  docker run -d --rm --name "$name" -e POSTGRES_USER=it -e POSTGRES_PASSWORD=it -e POSTGRES_DB=it -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
  trap 'docker stop "$name" >/dev/null' EXIT
  port=$(docker port "$name" 5432/tcp | head -1 | sed 's/.*://')
  until docker exec "$name" pg_isready -U it -d it >/dev/null 2>&1; do sleep 0.5; done
  sleep 1
  export TEST_DATABASE_URL="postgres://it:it@127.0.0.1:${port}/it?sslmode=disable"
fi

go test -count=1 -race -coverpkg=./internal/... -coverprofile=coverage-integration.out ./internal/api/... "$@"
go tool cover -func=coverage-integration.out | tail -1
