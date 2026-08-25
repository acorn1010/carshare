// Command bench measures the store's throughput at a given fleet size. It
// seeds cars server-side (generate_series, so 10M rows take minutes, not
// hours), then runs three timed scenarios through the real store code:
//
//   - availability: random searches across the city
//   - order: bookings of random cars at random windows, the happy path
//   - contended order: every worker fights for one car and window, the
//     conflict path the exclusion constraint serializes
//
// Usage:
//
//	DATABASE_URL=... go run ./cmd/bench -cars 100000 -duration 10s -workers 32
//
// Results are store-level QPS. HTTP adds a fixed per-request cost on top but
// no additional contention.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"carshare/internal/store"
)

// cityBox is roughly a 35km x 33km metro: lng -122.60..-122.20, lat
// 37.60..37.90. Density scales with -cars, which is the point.
const (
	cityLngWest  = -122.60
	cityLngSpan  = 0.40
	citySouthLat = 37.60
	cityLatSpan  = 0.30
)

func main() {
	carTarget := flag.Int("cars", 100_000, "fleet size to seed before measuring")
	duration := flag.Duration("duration", 10*time.Second, "measure time per scenario")
	workers := flag.Int("workers", 32, "concurrent workers")
	searchRadius := flag.Float64("radius", 2000, "availability search radius in meters")
	flag.Parse()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "set DATABASE_URL")
		os.Exit(1)
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	must(err)
	config.MaxConns = int32(*workers)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	must(err)
	defer pool.Close()
	dataStore := store.NewWithPool(pool)

	fmt.Printf("== seeding to %d cars\n", *carTarget)
	seed(ctx, pool, *carTarget)

	carIDs := sampleCarIDs(ctx, pool, 200_000)
	bookerID := benchUser(ctx, pool)
	fmt.Printf("== measuring with %d workers for %s per scenario\n", *workers, *duration)

	// The two sorts have different cost shapes and the gap is large, so they
	// are measured apart. Distance is what the API serves by default: it
	// early-exits out of the location index. Price has to rank the whole
	// circle before it knows the cheapest, so it pays for density.
	for _, sortMode := range []string{"distance", "price"} {
		runScenario(ctx, "availability ("+sortMode+")", *workers, *duration, func(generator *rand.Rand) error {
			start := time.Now().Add(time.Duration(1+generator.Intn(72)) * time.Hour)
			_, err := dataStore.Availability(ctx, store.AvailabilityParams{
				Lat:         citySouthLat + generator.Float64()*cityLatSpan,
				Lng:         cityLngWest + generator.Float64()*cityLngSpan,
				RangeMeters: *searchRadius,
				Start:       start,
				End:         start.Add(2 * time.Hour),
				Sort:        sortMode,
				Limit:       100,
			})
			return err
		})
	}

	// A count runs once per search rather than once per page, but it walks as
	// far as the deepest page a searcher can reach, so this is the number to
	// watch when judging what showing a result total costs.
	runScenario(ctx, "availability count", *workers, *duration, func(generator *rand.Rand) error {
		start := time.Now().Add(time.Duration(1+generator.Intn(72)) * time.Hour)
		_, err := dataStore.AvailabilityCount(ctx, store.AvailabilityCountParams{
			Lat:         citySouthLat + generator.Float64()*cityLatSpan,
			Lng:         cityLngWest + generator.Float64()*cityLngSpan,
			RangeMeters: *searchRadius,
			Start:       start,
			End:         start.Add(2 * time.Hour),
			Cap:         1001,
		})
		return err
	})

	runScenario(ctx, "order (random cars)", *workers, *duration, func(generator *rand.Rand) error {
		start := time.Now().Truncate(time.Hour).Add(time.Duration(24+generator.Intn(24*85)) * time.Hour)
		_, err := dataStore.OrderCar(ctx, store.OrderParams{
			CarID:    carIDs[generator.Intn(len(carIDs))],
			BookerID: bookerID,
			Kind:     store.KindRental,
			Start:    start,
			End:      start.Add(time.Hour),
			MaxPrice: 1 << 30,
		})
		if err == store.ErrConflict || err == store.ErrScheduleConflict {
			return nil // a real outcome, not a failure
		}
		return err
	})

	contendedStart := time.Now().Truncate(time.Hour).Add(30 * 24 * time.Hour)
	contendedCar := carIDs[0]
	runScenario(ctx, "order (one contended car)", *workers, *duration, func(generator *rand.Rand) error {
		_, err := dataStore.OrderCar(ctx, store.OrderParams{
			CarID:    contendedCar,
			BookerID: bookerID,
			Kind:     store.KindRental,
			Start:    contendedStart,
			End:      contendedStart.Add(time.Hour),
			MaxPrice: 1 << 30,
		})
		if err == store.ErrConflict {
			return nil
		}
		return err
	})
}

// seed tops the fleet up to target cars in 1M batches and rebuilds the seeded
// reservation load: about 20% of cars carry one future 2 hour booking, so
// availability pays a realistic anti-join cost.
func seed(ctx context.Context, pool *pgxpool.Pool, target int) {
	_, err := pool.Exec(ctx, `
		INSERT INTO cars.users (email, display_name)
		SELECT 'bench-owner-' || g || '@example.com', 'bench-owner-' || g
		FROM generate_series(1, 1000) g
		WHERE NOT EXISTS (SELECT 1 FROM cars.users WHERE display_name = 'bench-owner-1')`)
	must(err)

	var existing int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM cars.cars`).Scan(&existing))
	for existing < target {
		batch := min(target-existing, 1_000_000)
		started := time.Now()
		_, err := pool.Exec(ctx, `
			WITH owner_pool AS (
				SELECT array_agg(id) AS ids FROM cars.users WHERE display_name LIKE 'bench-owner-%'
			)
			INSERT INTO cars.cars (owner_id, location, price_per_hour)
			SELECT ids[1 + (g % array_length(ids, 1))],
			       point($2 + random() * $3, $4 + random() * $5),
			       500 + (random() * 4500)::int
			FROM owner_pool, generate_series(1, $1) g`,
			batch, cityLngWest, cityLngSpan, citySouthLat, cityLatSpan)
		must(err)
		existing += batch
		fmt.Printf("   %d cars (+%d in %s)\n", existing, batch, time.Since(started).Round(time.Second))
	}

	fmt.Println("   reseeding reservations (20% of cars, one future booking each)")
	_, err = pool.Exec(ctx, `TRUNCATE cars.reservations`)
	must(err)
	booker := benchUser(ctx, pool)
	_, err = pool.Exec(ctx, `
		INSERT INTO cars.reservations (car_id, booker_id, kind, during, price)
		SELECT c.id, $1, 'rental', tstzrange(t.s, t.s + interval '2 hours'), 1000
		FROM (SELECT id FROM cars.cars TABLESAMPLE BERNOULLI (20)) c
		CROSS JOIN LATERAL (SELECT now() + (random() * 720)::int * interval '1 hour' AS s) t`,
		booker)
	must(err)
	_, err = pool.Exec(ctx, `ANALYZE cars.cars, cars.reservations`)
	must(err)
}

func benchUser(ctx context.Context, pool *pgxpool.Pool) string {
	var id string
	must(pool.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO cars.users (email, display_name)
			SELECT 'bench-booker@example.com', 'bench-booker'
			WHERE NOT EXISTS (SELECT 1 FROM cars.users WHERE display_name = 'bench-booker')
			RETURNING id
		)
		SELECT id FROM created
		UNION ALL
		SELECT id FROM cars.users WHERE display_name = 'bench-booker'
		LIMIT 1`).Scan(&id))
	return id
}

func sampleCarIDs(ctx context.Context, pool *pgxpool.Pool, limit int) []string {
	rows, err := pool.Query(ctx, `SELECT id FROM cars.cars LIMIT $1`, limit)
	must(err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		must(rows.Scan(&id))
		ids = append(ids, id)
	}
	must(rows.Err())
	return ids
}

// runScenario hammers the operation from every worker for the wall-clock
// duration and prints QPS plus latency percentiles.
func runScenario(ctx context.Context, name string, workers int, duration time.Duration, operation func(*rand.Rand) error) {
	deadline := time.Now().Add(duration)
	var operations, failures atomic.Int64
	var errOnce sync.Once
	latencies := make([][]time.Duration, workers)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			generator := rand.New(rand.NewSource(int64(worker)))
			for time.Now().Before(deadline) {
				started := time.Now()
				err := operation(generator)
				latencies[worker] = append(latencies[worker], time.Since(started))
				operations.Add(1)
				if err != nil {
					failures.Add(1)
					errOnce.Do(func() { fmt.Printf("   first error: %v\n", err) })
				}
			}
		}(worker)
	}
	waitGroup.Wait()

	var all []time.Duration
	for _, workerLatencies := range latencies {
		all = append(all, workerLatencies...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	fmt.Printf("%-26s %8.0f qps   p50 %-8s p95 %-8s p99 %-8s errors %d\n",
		name,
		float64(operations.Load())/duration.Seconds(),
		percentile(all, 0.50), percentile(all, 0.95), percentile(all, 0.99),
		failures.Load())
}

func percentile(sorted []time.Duration, quantile float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(float64(len(sorted)-1) * quantile)
	return sorted[index].Round(10 * time.Microsecond)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
