package httpapi

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"carshare/internal/store"
)

const (
	// searchCacheTTL is how stale a search page may be. A car booked inside
	// this window can still show in search; ordering it returns the conflict.
	searchCacheTTL = 30 * time.Second
	// searchCacheMaxEntries caps memory at roughly maxEntries * pageSize rows.
	searchCacheMaxEntries = 2048
	// searchCellDegrees is the snap grid, ~550m of latitude. Searchers within
	// a cell see results measured from the same cell corner.
	searchCellDegrees = 0.005
	// searchTimeStep snaps the trip window outward, so the cached window
	// covers the requested one and never shows a car busy inside it.
	searchTimeStep = 15 * time.Minute
	// searchRangeStep snaps the radius so slider jitter cannot fragment keys.
	searchRangeStep = 500.0
)

// snappedSearch is a search quantized onto the cache grid: the values to
// query the store with, and the cache key they share.
type snappedSearch struct {
	key string
	// countKey names the same search without its sort or page, because how
	// many cars match depends on neither. Paging through a result set reuses
	// one count, and flipping the sort pills reuses it too.
	countKey    string
	lat         float64
	lng         float64
	rangeMeters float64
	start       time.Time
	end         time.Time
}

// snapSearch quantizes a search so nearby requests share a cache entry. The
// window snaps outward (start down, end up), which can only hide a car that
// is busy just outside the requested window, never show a busy one.
func snapSearch(lat, lng, rangeMeters float64, from time.Time, duration time.Duration, sort string, page int) snappedSearch {
	snapped := snappedSearch{
		lat:         math.Round(lat/searchCellDegrees) * searchCellDegrees,
		lng:         math.Round(lng/searchCellDegrees) * searchCellDegrees,
		rangeMeters: math.Round(rangeMeters/searchRangeStep) * searchRangeStep,
		start:       from.Truncate(searchTimeStep),
	}
	snapped.end = from.Add(duration).Truncate(searchTimeStep)
	if snapped.end.Before(from.Add(duration)) {
		snapped.end = snapped.end.Add(searchTimeStep)
	}
	snapped.countKey = fmt.Sprintf("%.4f|%.4f|%.0f|%d|%d",
		snapped.lat, snapped.lng, snapped.rangeMeters, snapped.start.Unix(), snapped.end.Unix())
	snapped.key = fmt.Sprintf("%s|%s|%d", snapped.countKey, sort, page)
	return snapped
}

type searchCacheEntry[T any] struct {
	value   T
	expires time.Time
}

// searchCache memoizes one kind of search result. Search tolerates a little
// staleness (booking re-checks the truth in the database), and snapping means
// nearby searchers share one entry, so database load is bounded by distinct
// cells per TTL instead of by user traffic. It is a TTL map with
// single-flight fills: concurrent misses on one key run the query once and
// share the result. Values handed out are shared, so callers must copy before
// mutating.
//
// It is generic over the value because pages and match counts cache on
// different keys: a page key names one page, a count key names the whole
// search, so paging through a result set counts once rather than once a page.
type searchCache[T any] struct {
	mutex    sync.Mutex
	entries  map[string]searchCacheEntry[T]
	inflight map[string]chan struct{}
}

func newSearchCache[T any]() *searchCache[T] {
	return &searchCache[T]{
		entries:  make(map[string]searchCacheEntry[T]),
		inflight: make(map[string]chan struct{}),
	}
}

// Do returns the cached value for key, or runs fill exactly once across
// concurrent misses and caches what it returns. hit reports whether the value
// came from cache. Errors are returned to every waiter and never cached.
func (cache *searchCache[T]) Do(ctx context.Context, key string, fill func() (T, error)) (value T, hit bool, err error) {
	for {
		cache.mutex.Lock()
		if entry, ok := cache.entries[key]; ok && time.Now().Before(entry.expires) {
			cache.mutex.Unlock()
			return entry.value, true, nil
		}
		filling, ok := cache.inflight[key]
		if !ok {
			cache.inflight[key] = make(chan struct{})
			cache.mutex.Unlock()
			break
		}
		cache.mutex.Unlock()
		select {
		case <-filling:
		case <-ctx.Done():
			var zero T
			return zero, false, ctx.Err()
		}
	}

	value, err = fill()
	cache.mutex.Lock()
	if err == nil {
		cache.evictIfFull()
		cache.entries[key] = searchCacheEntry[T]{value: value, expires: time.Now().Add(searchCacheTTL)}
	}
	close(cache.inflight[key])
	delete(cache.inflight, key)
	cache.mutex.Unlock()
	return value, false, err
}

// personalizeSearch copies a cached page (the rows are shared) and replaces
// the numbers the snapping distorted: distance is remeasured from the exact
// search point instead of the cell corner, and the trip price is repriced for
// the requested duration instead of the snapped-outward window. Then it
// restores the requested order with the fresh numbers. Without this, prices
// would inflate by up to two snap steps and distances would jump as a
// searcher crosses cell borders.
func personalizeSearch(cached []store.AvailableCar, lat, lng float64, duration time.Duration, sortMode string) []store.AvailableCar {
	results := slices.Clone(cached)
	for i := range results {
		results[i].DistanceMeters = store.DistanceMeters(lat, lng, results[i].Lat, results[i].Lng)
		results[i].TripPrice = int(math.Round(float64(results[i].PricePerHour) * duration.Hours()))
	}
	slices.SortFunc(results, func(first, second store.AvailableCar) int {
		byDistance := cmp.Compare(first.DistanceMeters, second.DistanceMeters)
		byPrice := cmp.Compare(first.TripPrice, second.TripPrice)
		if sortMode == "distance" {
			return cmp.Or(byDistance, byPrice, cmp.Compare(first.ID, second.ID))
		}
		return cmp.Or(byPrice, byDistance, cmp.Compare(first.ID, second.ID))
	})
	return results
}

// evictIfFull drops expired entries once the cache is full, then arbitrary
// ones if that was not enough. Entries only live 30 seconds, so anything
// fancier than map order would never earn its keep. Caller holds the mutex.
func (cache *searchCache[T]) evictIfFull() {
	if len(cache.entries) < searchCacheMaxEntries {
		return
	}
	now := time.Now()
	for key, entry := range cache.entries {
		if now.After(entry.expires) {
			delete(cache.entries, key)
		}
	}
	for key := range cache.entries {
		if len(cache.entries) < searchCacheMaxEntries {
			break
		}
		delete(cache.entries, key)
	}
}
