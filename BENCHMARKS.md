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

**Search cost is density, not fleet size.** Availability tracks how many cars sit inside the radius, roughly 100 candidates per search at 1k cars and ~109,000 at 10M (a 2km radius over 8,600 cars/km², about 25x Manhattan's taxi density). Same fleet with a 500m radius is 10x faster. The levers, in the order a real product would pull them: shrink the radius as density grows (users in dense cities do not need a 2km circle), cache search results for a few seconds (bookings shift availability slowly at any given minute), and shard by city when a single metro genuinely holds millions of cars.

## The contention war story

The first run of the contended scenario produced 4 qps, 20-second p50 latencies, and `deadlock detected` errors. Not a bug in the constraint, a property of it: exclusion constraints check conflicts by inserting first and scanning second, so two concurrent same-window inserts each find the other's uncommitted row, each waits for the other's transaction, and Postgres has to break the cycle with a 40P01. Under a sustained pile-up the losers form a long wait chain.

Two changes in [internal/store/store.go](internal/store/store.go) fixed it, and neither touches correctness:

1. `pg_advisory_xact_lock(hash(car_id))` as the first statement of the booking transaction. Writers on the same car queue in order instead of dogpiling the constraint, writers on different cars never meet, and the deadlock cycle becomes impossible.
2. An indexed `EXISTS` precheck inside the transaction that answers doomed attempts in microseconds instead of letting them attempt the insert.

Result: 4 qps with errors became ~1,200 qps with zero errors. The exclusion constraint stays the only correctness authority, anything racing past the precheck still dies there.
