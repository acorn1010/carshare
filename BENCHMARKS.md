# Benchmarks

## Where it stands today

Re-measured 2026-08-25 on one 24-core dev machine (WSL2, Postgres 16 in Docker, load generator on the same box). One API pod against a 400k-car fleet with ~26,000 cars inside a 10km Manhattan search circle, 30% of them busy over the searched window.

| what | number | measured how |
|---|---|---|
| search, any traffic pattern | **6,200-13,100/s per pod**, worst case included | `./scripts/bench.sh http`, ["The read model war story"](#the-read-model-war-story) |
| search freshness | **~250ms** behind the database | the fleet_log poll |
| search load on Postgres | **zero** | search runs from each pod's in-memory fleet |
| store-level search SQL, closest sort | 6,300-8,400 qps, p50 3-4ms | `./scripts/bench.sh store`, kept for the bench and history |
| bookings, fleet-wide | **~4,500/s**, p50 ~7ms | same, any fleet size |
| bookings, one contended car | **~1,100/s** | the per-car serialization ceiling, by design |
| where search started | 52/s | the deleted rank-everything sort, ["The search war story"](#the-search-war-story) |

Search has no hot or cold path anymore: every request recomputes from the pod's in-memory fleet, so the worst-case pattern and the best differ by 2x, not 100x, and search capacity is pods times these numbers with no shared bottleneck behind them.

Same machine ran the load generator, so read the absolutes as conservative floors and trust the ratios.

## Reproduce

```bash
./scripts/bench.sh store            # store ceilings at 1k, 100k, and 1M cars
./scripts/bench.sh store 10000000   # the 10M stress row, minutes of seeding
./scripts/bench.sh http             # full-stack searches/s (needs wrk)
```

The script starts its own tuned Postgres 16 container (4GB shared_buffers) and removes it when done, so it never touches the dev database. `store` runs [cmd/bench](cmd/bench): server-side seeding, then 32 workers hammering each scenario through the real store code for 10s. It prints a `SELECT 1` ceiling first, because the fastest rows below are bounded by client round-trips, not by Postgres.

## Store-level results by fleet size

Measured 2026-08-25, 32 workers, 2km radius, all cars in one ~35x33km city so density grows with fleet size. 20% of cars carry one future booking. The no-op ceiling on this box was 289k tps in-container.

| fleet | search (distance) | search (price) | result count | order, random cars | order, contended |
|---|---|---|---|---|---|
| 1k | 26,598 qps, p50 1.0ms | 27,039 qps, p50 1.0ms | 31,572 qps | 4,781 qps, p50 6.3ms | 1,119 qps, p50 28ms |
| 100k | 8,377 qps, p50 3.4ms | 3,340 qps, p50 8.5ms | 3,712 qps | 4,704 qps, p50 6.5ms | 1,172 qps, p50 27ms |
| 1M | 6,335 qps, p50 4.4ms | 560 qps, p50 52ms | 2,508 qps | 4,460 qps, p50 6.8ms | 1,080 qps, p50 30ms |

At 1k cars every query is sub-millisecond, so that row measures round-trip overhead and is best read as "faster than the harness can see". Earlier revisions of this table (including 10M-car runs against a query shape that no longer exists) are in git history.

## What the numbers say

**Booking throughput does not care how big the fleet is.** ~4,500 bookings/s at every fleet size, p50 ~7ms throughout. The whole write path is per-car index work: the advisory lock, the hold reap, the conflict precheck, and the exclusion check all touch one car's rows. A million other cars are invisible to it. For scale context, 4,500 bookings/s is ~390M bookings a day on one box.

**One contended car serializes at ~1,100 bookings/s.** That is the per-car ceiling by design: two bookings for the same car must serialize somewhere, and this system does it on a per-car advisory lock inside Postgres. No human fleet has a car that a thousand people try to book per second.

**Distance search is nearly flat across fleet size, price search is not.** Closest-first streams from the location index and stops when the page fills, so 1k to 1M cars only moves it from 27k to 6.3k qps. Cheapest-first must rank candidates by price before it knows which to check, and no index serves that order, so its cost tracks how many cars sit in the circle: 560 qps at 1M. That asymmetry is why closest is the product default and cheapest ranks a bounded candidate pool (see `availabilityByPriceCandidateSQL`).

## How big is a real city, anyway

For calibration against the real world: New York, the densest for-hire market on earth, runs 13,587 yellow medallions plus roughly 130,000 total TLC-licensed vehicles. Turo's entire fleet across every market it operates in was about 340,000 active vehicles at the end of 2024. So a mega-successful car-share's largest single city is realistically 50,000 to 150,000 cars, with "half of every registered car in NYC" (~1M) as the absurd theoretical ceiling. Read the table with that in mind: the 100k row, 8,400 searches/s at p50 3.4ms straight from SQL, *is* the mega-success case on one Postgres. And the in-memory fleet now answers searches before they ever reach it.

## The contention war story

The first run of the contended scenario produced 4 qps, 20-second p50 latencies, and `deadlock detected` errors. Not a bug in the constraint, a property of it: exclusion constraints check conflicts by inserting first and scanning second, so two concurrent same-window inserts each find the other's uncommitted row, each waits for the other's transaction, and Postgres has to break the cycle with a 40P01. Under a sustained pile-up the losers form a long wait chain.

Two changes in [internal/store/store.go](internal/store/store.go) fixed it, and neither touches correctness:

1. `pg_advisory_xact_lock(hash(car_id))` as the first statement of the booking transaction. Writers on the same car queue in order instead of dogpiling the constraint, writers on different cars never meet, and the deadlock cycle becomes impossible.
2. An indexed `EXISTS` precheck inside the transaction that answers doomed attempts in microseconds instead of letting them attempt the insert.

Result: 4 qps with errors became ~1,100 qps with zero errors. The exclusion constraint stays the only correctness authority, anything racing past the precheck still dies there.

## The search war story

Search originally ranked every car in the circle to serve any page, because the default sort was cheapest-first and price order is the one thing the location index cannot stream. In the dense-NYC scenario that meant 462ms per search and 52/s at saturation. Letting the sort be closest-first turned the same query into an index walk that stops when the page fills: 8.7ms, and the store now sustains thousands of searches/s at any density. Cheapest survives as an option that ranks a bounded candidate pool instead of the whole circle, trading occasional short pages for never scanning 26k rows.

Getting the planner to pick the streaming plan took one non-obvious move, documented on the SQL in [internal/store/store.go](internal/store/store.go): the exact-meter radius trim must stay out of the KNN subquery. As an opaque expression it gets a fixed selectivity guess, the row estimate falls below the LIMIT, and the planner trades the ordered index scan for scan-everything-and-sort, 264ms instead of 8. The trim lives in the outer query, where it cannot influence the inner plan and can only shave a page's tail.

The same trap bit a second time. The "how many cars matched" count originally reused the KNN ordering so count and pagination would agree exactly, its LIMIT sat far above the circle's row estimate, and the planner sorted the whole circle: 270ms per count, and since a count runs on every cold cell, the cache-miss path collapsed to 65 searches/s. The count now runs unordered, any plan can stop at its cap, and the miss path came back to ~380-570/s. The lesson generalizes: near a LIMIT, every new query needs checking against which side of the planner's row estimate it lands on.

On top of the queries sat a 30-second cache, since retired by the read model in the next chapter. Searches snapped to a ~550m cell, a 15-minute-aligned window, and a 500m range step, so nearby searchers share one database query, with single-flight fills so a stampede on a cold cell runs it once. Every response then re-measures distances and re-prices the trip from the exact request, so the snapping never shows. Database load stops scaling with user traffic and is bounded by distinct busy cells per 30 seconds. The trade: a car booked seconds ago can linger in search for up to 30 seconds. Ordering it returns the same JUST TAKEN conflict the two-tab race demonstrates, and the exclusion constraint stays the only truth.

Serialization was the last bill. Once the cache absorbs the database work, half the remaining CPU was encoding/json formatting search pages, much of it printing 17-digit floats nobody reads. Search rows now carry only what search shows (dropping owner_id, which a public endpoint had no business sharing), coordinates round to 6 decimals (~11cm) and distance to whole meters. Pages shrank from 25.7KB to 16KB and one pod serves ~45,000 cached searches/s end to end.

## The read model war story

The cache had one flaw left: it was a cache. Hot cells were nearly free, cold cells cost a database round trip, and an attacker who kept minting cold cells lived at the 380/s floor. The fix was to stop asking Postgres at all. Every pod now holds the whole fleet in memory ([internal/fleet](internal/fleet)): 400k cars and their 1.1M future busy windows load from one repeatable-read snapshot in ~3 seconds at boot (476MB resident), and stay current by tailing `cars.fleet_log`, an append-only change feed that schema triggers write in the same transaction as every car, reservation, and schedule change. Postgres keeps its one irreplaceable job: the exclusion constraint deciding who wins the car.

Same NYC fixture, same wrk scenarios, one pod:

| scenario | with the 30s cache | in-memory fleet |
|---|---|---|
| hot cell (identical queries) | 45,200/s | 13,100/s |
| spread over the metro | 3,700/s | 6,500/s |
| every request unique | **380/s** | **6,200/s** |

Read the middle column honestly: the cache beat the fleet on byte-identical repeats, because serving a stored page is cheaper than recomputing one. Everywhere else the fleet wins, and the bottom row is the story: the worst case improved 16x and now equals the average case. There is no cold path left to attack, no hot path to lose, and the database's search load is exactly zero, so pods scale search linearly with no shared bottleneck behind them. Freshness also improved 100x: results now trail reality by the feed's ~250ms poll instead of the cache's 30 seconds, so a car booked in one tab disappears from the next search in the other.

The replication design is the part worth reading. The log's sequence numbers commit out of order (a slow transaction can commit a low seq after a faster one committed a higher seq), so the cursor trails a 2-second stability lag and re-applies the tail on every pull, which is free because applies are idempotent. Boot takes one snapshot and starts the cursor at the newest stable row, so changes racing the snapshot get re-applied rather than lost. A broken feed rebuilds from a fresh snapshot while the old model keeps serving. The Go port of the recurrence math is pinned to the SQL by running both against the same DST and month-end cases. And the trade stays the one this repo has always made: search may be a quarter-second stale, booking never is.
