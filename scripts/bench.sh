#!/usr/bin/env bash
# Reproduces BENCHMARKS.md.
#
#   ./scripts/bench.sh store [fleet sizes...]   store-level ceilings via cmd/bench
#   ./scripts/bench.sh http                     full-stack searches/s via wrk
#
# store defaults to 1k, 100k, and 1M cars. Pass 10000000 yourself if you have
# ten minutes to spare for seeding. http needs wrk on the PATH.
#
# Every run starts its own tuned Postgres 16 container and removes it on exit,
# so it never touches the dev database from scripts/dev_db.sh.
set -euo pipefail
cd "$(dirname "$0")/.."

MODE="${1:-store}"
shift || true

CONTAINER=carshare-bench
PORT=5439
URL="postgres://postgres:bench@127.0.0.1:${PORT}/carshare?sslmode=disable"

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]]; then
    kill "${SERVER_PID}" 2>/dev/null || true
  fi
  docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
docker run -d --name "${CONTAINER}" \
  -e POSTGRES_PASSWORD=bench -e POSTGRES_DB=carshare \
  -p "${PORT}:5432" postgres:16 \
  -c shared_buffers=4GB -c effective_cache_size=24GB >/dev/null
until docker exec "${CONTAINER}" pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
docker exec -i "${CONTAINER}" psql -U postgres -d carshare -q -v ON_ERROR_STOP=1 < db/schema.sql

case "${MODE}" in
store)
  # The no-op ceiling first: SELECT 1 at the same concurrency. No real query
  # can beat it, so it says how much of a fast row is round-trip overhead on
  # this machine, not Postgres. Runs inside the container, so treat it as an
  # upper bound on what host clients can see.
  echo "== no-op ceiling (SELECT 1, 32 clients)"
  docker exec "${CONTAINER}" bash -c \
    'echo "SELECT 1;" > /tmp/noop.sql && pgbench -n -f /tmp/noop.sql -c 32 -j 12 -T 5 -U postgres carshare' \
    | grep -E "tps|latency average"

  for cars in "${@:-1000 100000 1000000}"; do
    DATABASE_URL="${URL}" go run ./cmd/bench -cars "${cars}" -duration 10s -workers 32
  done
  ;;

http)
  command -v wrk >/dev/null || { echo "http mode needs wrk installed" >&2; exit 1; }
  WORK="$(mktemp -d)"

  echo "== seeding the NYC worst case (400k cars, ~26k inside the search circle)"
  docker exec -i "${CONTAINER}" psql -U postgres -d carshare -q -v ON_ERROR_STOP=1 <<'SQL'
INSERT INTO cars.users (id, display_name)
VALUES ('00000000-0000-0000-0000-000000000001', 'bench-owner');
-- 150k cars in a 0.5 degree box on Manhattan, 250k across 39 other metros.
INSERT INTO cars.cars (owner_id, model, location, price_per_hour)
SELECT '00000000-0000-0000-0000-000000000001', 'Bench Car',
       point(-73.99 + (random() - 0.5) * 0.5, 40.75 + (random() - 0.5) * 0.5),
       (500 + random() * 4500)::int
FROM generate_series(1, 150000);
INSERT INTO cars.cars (owner_id, model, location, price_per_hour)
SELECT '00000000-0000-0000-0000-000000000001', 'Bench Car',
       point(-120 + ((1 + g % 39) / 8) * 10 + (random() - 0.5) * 0.5,
             25   + ((1 + g % 39) % 8) * 5  + (random() - 0.5) * 0.5),
       (500 + random() * 4500)::int
FROM generate_series(1, 250000) g;
-- Two future bookings per car, and 30% also busy over the searched window.
INSERT INTO cars.reservations (car_id, booker_id, kind, during, price, status)
SELECT c.id, '00000000-0000-0000-0000-000000000001', 'rental',
       tstzrange(now() + interval '200 hours' + n * interval '10 hours',
                 now() + interval '202 hours' + n * interval '10 hours'),
       1000, 'confirmed'
FROM cars.cars c, generate_series(0, 1) n;
INSERT INTO cars.reservations (car_id, booker_id, kind, during, price, status)
SELECT c.id, '00000000-0000-0000-0000-000000000001', 'rental',
       tstzrange(now() + interval '71 hours', now() + interval '77 hours'),
       1000, 'confirmed'
FROM cars.cars c WHERE random() < 0.3;
ANALYZE cars.cars; ANALYZE cars.reservations;
SQL

  echo "== starting the API"
  go build -o "${WORK}/carshare" ./cmd/carshare
  DATABASE_URL="${URL}" PORT=3200 METRICS_PORT=9201 LOG_LEVEL=warn \
    "${WORK}/carshare" >"${WORK}/server.log" 2>&1 &
  SERVER_PID=$!
  until curl -sf http://127.0.0.1:3200/health >/dev/null 2>&1; do sleep 0.5; done

  FROM="$(date -u -d '+72 hours' +%Y-%m-%dT%H:00:00Z)"
  cat > "${WORK}/hot.lua" <<LUA
request = function()
  return wrk.format("GET", "/v1/availability?lat=40.7500&lng=-73.9900&from=${FROM}&duration_minutes=120")
end
LUA
  cat > "${WORK}/spread.lua" <<LUA
math.randomseed(os.clock() * 1000000)
request = function()
  local lat = 40.55 + math.random() * 0.40
  local lng = -74.15 + math.random() * 0.32
  return wrk.format("GET", string.format("/v1/availability?lat=%.4f&lng=%.4f&from=${FROM}&duration_minutes=120", lat, lng))
end
LUA
  cat > "${WORK}/miss.lua" <<LUA
math.randomseed(os.clock() * 1000000)
local i = math.random(1, 100000)
request = function()
  i = i + 1
  local lat = 40.55 + math.random() * 0.40
  local lng = -74.15 + math.random() * 0.32
  local dur = 60 + (i % 480) * 15
  return wrk.format("GET", string.format("/v1/availability?lat=%.4f&lng=%.4f&from=${FROM}&duration_minutes=%d", lat, lng, dur))
end
LUA

  for scenario in hot spread miss; do
    echo "== ${scenario} (12 threads, 200 connections, 15s)"
    wrk -t12 -c200 -d15s -s "${WORK}/${scenario}.lua" http://127.0.0.1:3200 \
      | grep -E "Requests/sec|Latency|Non-2xx|Socket"
  done
  rm -rf "${WORK}"
  ;;

*)
  echo "usage: $0 store [fleet sizes...] | http" >&2
  exit 1
  ;;
esac
