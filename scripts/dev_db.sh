#!/usr/bin/env bash
# Starts (or stops with --stop) the local Postgres used by development and the
# integration tests. Exists so tests run against a real Postgres, not a mock:
# the exclusion constraint and recurrence SQL are the parts worth testing.
set -euo pipefail

CONTAINER=carshare-postgres
PORT=5434

if [[ "${1:-}" == "--stop" ]]; then
  docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
  echo "stopped ${CONTAINER}"
  exit 0
fi

if ! docker start "${CONTAINER}" >/dev/null 2>&1; then
  docker run -d --name "${CONTAINER}" \
    -e POSTGRES_PASSWORD=carshare \
    -e POSTGRES_DB=carshare \
    -p "${PORT}:5432" \
    postgres:16 >/dev/null
fi

for _ in $(seq 1 30); do
  if docker exec "${CONTAINER}" pg_isready -U postgres >/dev/null 2>&1; then
    echo "postgres ready on 127.0.0.1:${PORT}"
    echo "export CARSHARE_TEST_DATABASE_URL=postgres://postgres:carshare@127.0.0.1:${PORT}/carshare?sslmode=disable"
    exit 0
  fi
  sleep 1
done

echo "postgres did not become ready" >&2
exit 1
