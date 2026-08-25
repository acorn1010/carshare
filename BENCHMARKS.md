# Benchmarks

Store-level throughput at four fleet sizes, measured with [cmd/bench](cmd/bench) against Postgres 16 in Docker (4GB shared_buffers, 24GB effective_cache_size) on a 24-core machine, 32 workers, 10s per scenario. All cars sit in one ~35x33km city so density grows with fleet size, which is the interesting variable. 20% of cars carry one future booking so search pays a realistic anti-join cost.

Reproduce with:

```bash
./scripts/dev_db.sh
DATABASE_URL=postgres://postgres:carshare@127.0.0.1:5434/carshare?sslmode=disable \
  go run ./cmd/bench -cars 1000000 -duration 10s -workers 32
```

## Results

| fleet | availability (2km radius) | order, random cars | order, one contended car |
|---|---|---|---|
| 1k cars | 27,809 qps, p50 1.0ms | 4,867 qps, p50 6.2ms | 1,053 qps, p50 30ms |
| 100k cars | 2,584 qps, p50 11ms | 4,594 qps, p50 6.6ms | 1,233 qps, p50 26ms |
| 1M cars | 598 qps, p50 48ms | 4,853 qps, p50 6.3ms | 1,285 qps, p50 25ms |
| 10M cars | 43 qps, p50 583ms | 4,042 qps, p50 7.3ms | 1,179 qps, p50 27ms |
| 10M cars, 500m radius | 409 qps, p50 63ms | 3,281 qps | 708 qps |

## What the numbers say

**Booking throughput does not care how big the fleet is.** ~4-5,000 bookings/s at 1k cars and still ~4,000/s at 10M, p50 ~7ms throughout. The whole write path is per-car index work: the advisory lock, the hold reap, the conflict precheck, and the exclusion check all touch one car's rows. Ten million other cars are invisible to it. For scale context, 4,000 bookings/s is ~350M bookings a day on one box.

**One contended car serializes at ~1,200 bookings/s.** That is the per-car ceiling by design: two bookings for the same car must serialize somewhere, and this system does it on a per-car advisory lock inside Postgres. No human fleet has a car that 1,200 people try to book per second.

**Search cost is density, not fleet size.** Availability tracks how many cars sit inside the radius, roughly 100 candidates per search at 1k cars and ~109,000 at 10M (a 2km radius over 8,600 cars/km², about 25x Manhattan's taxi density). Same fleet with a 500m radius is 10x faster. The levers, in the order a real product would pull them: shrink the radius as density grows (users in dense cities do not need a 2km circle), cache search results for a few seconds (bookings shift availability slowly at any given minute), and shard by city when a single metro genuinely holds millions of cars. Two stronger levers have been pulled since these numbers were taken, see "The search war story" below: these rows price the rank-everything cheapest sort, and the closest-first default plus the 30-second cache changes the story entirely.

## How big is a real city, anyway

For calibration against the real world: New York, the densest for-hire market on earth, runs 13,587 yellow medallions plus roughly 130,000 total TLC-licensed vehicles. Turo's entire fleet across every market it operates in was about 340,000 active vehicles at the end of 2024. So a mega-successful car-share's largest single city is realistically 50,000 to 150,000 cars, with "half of every registered car in NYC" (~1M) as the absurd theoretical ceiling. Read the table with that in mind: the 100k row, 2,600 searches/s at p50 11ms with no cache, *is* the mega-success case on one Postgres. The 10M row is a stress test roughly 100x past anything that exists.

## The contention war story

The first run of the contended scenario produced 4 qps, 20-second p50 latencies, and `deadlock detected` errors. Not a bug in the constraint, a property of it: exclusion constraints check conflicts by inserting first and scanning second, so two concurrent same-window inserts each find the other's uncommitted row, each waits for the other's transaction, and Postgres has to break the cycle with a 40P01. Under a sustained pile-up the losers form a long wait chain.

Two changes in [internal/store/store.go](internal/store/store.go) fixed it, and neither touches correctness:

1. `pg_advisory_xact_lock(hash(car_id))` as the first statement of the booking transaction. Writers on the same car queue in order instead of dogpiling the constraint, writers on different cars never meet, and the deadlock cycle becomes impossible.
2. An indexed `EXISTS` precheck inside the transaction that answers doomed attempts in microseconds instead of letting them attempt the insert.

Result: 4 qps with errors became ~1,200 qps with zero errors. The exclusion constraint stays the only correctness authority, anything racing past the precheck still dies there.

## The search war story

The table above prices every search the way the cheapest-first sort must: rank every car in the circle, then take a page. Cheapest is the one ordering the location index cannot stream, so its cost grows with density. Sorting by distance lets Postgres walk the index nearest-first and stop the moment the page is full. Filling a page of 100 free cars usually means touching a few hundred rows, whether the circle holds a thousand cars or twenty-six thousand.

Measured with the store's real SQL on a 400k-car fleet, 26,000 cars inside a 10km circle around Manhattan, 30% of them booked over the search window, 24 concurrent clients on a 24-core box:

| sort | p50 latency | searches/s |
|---|---|---|
| cheapest first (rank everything) | 462ms | 52 |
| closest first (stream from the index) | 8.7ms | 2,760 |

Same table, same indexes, same filters. The product decision that unlocks it: closest is now the default sort, and cheapest stays one tap away.

Getting the planner to pick the streaming plan took one non-obvious move, documented on the SQL in [internal/store/store.go](internal/store/store.go). The exact-meter radius trim must stay out of the KNN subquery. As an opaque expression it gets a fixed selectivity guess, the row estimate falls below the LIMIT, and the planner trades the ordered index scan for scan-everything-and-sort, 264ms instead of 8. The trim lives in the outer query, where it cannot influence the inner plan and can only shave a page's tail.

On top of the query sits a 30-second cache ([internal/httpapi/searchcache.go](internal/httpapi/searchcache.go)). Searches snap to a ~550m cell, a 15-minute-aligned window, and a 500m range step, so nearby searchers share one database query, with single-flight fills so a stampede on a cold cell runs it once. Every response then re-measures distances and re-prices the trip from the exact request, so the snapping never shows. The effect on capacity: database load stops scaling with user traffic and is bounded by distinct busy cells per 30 seconds. The trade: a car booked seconds ago can linger in search for up to 30 seconds. Ordering it returns the same JUST TAKEN conflict the two-tab race demonstrates, and the exclusion constraint stays the only truth.
