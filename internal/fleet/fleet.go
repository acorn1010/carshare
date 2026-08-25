// Package fleet holds every car and its future busy windows in memory and
// answers searches from RAM. Postgres stays the only truth for booking (the
// exclusion constraint), the fleet is a read model fed by cars.fleet_log and
// rebuilt from a snapshot whenever the feed breaks. Search staleness is the
// feed lag, usually well under a second, and a stale answer costs at most a
// booking conflict, same as the old 30-second cache but ~30x fresher.
package fleet

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"carshare/internal/store"
)

// cellDegrees buckets cars into a grid so a search only visits cells its
// circle touches. ~2.2km of latitude per cell.
const cellDegrees = 0.02

type window struct {
	id            string
	start, end    time.Time
	holdExpiresAt *time.Time
}

type car struct {
	id           string
	model        string
	modelYear    *int
	lat, lng     float64
	pricePerHour int
	listed       bool
	cell         int64
	busy         []window
	recurrences  []recurrence
}

// model is one generation of the fleet. All access goes through Fleet's lock.
type model struct {
	cars  map[string]*car
	cells map[int64]map[string]*car
	// reservationCar and recurrenceCar find the owning car when a change
	// entry removes a window or schedule.
	reservationCar map[string]string
	recurrenceCar  map[string]string
}

func newModel() *model {
	return &model{
		cars:           make(map[string]*car),
		cells:          make(map[int64]map[string]*car),
		reservationCar: make(map[string]string),
		recurrenceCar:  make(map[string]string),
	}
}

func cellOf(lat, lng float64) int64 {
	return int64(int32(math.Floor(lat/cellDegrees)))<<32 | int64(uint32(int32(math.Floor(lng/cellDegrees))))
}

// Fleet is the in-memory search model plus its feed position.
type Fleet struct {
	mu      sync.RWMutex
	model   *model
	lastSeq int64
}

// available reports whether the car can be booked for the whole window,
// mirroring the SQL filter: listed, no live confirmed window overlapping, no
// recurrence occurrence overlapping. Expired holds do not block.
func (c *car) available(start, end, now time.Time) bool {
	if !c.listed {
		return false
	}
	for _, busy := range c.busy {
		if busy.holdExpiresAt != nil && !busy.holdExpiresAt.After(now) {
			continue
		}
		if busy.start.Before(end) && start.Before(busy.end) {
			return false
		}
	}
	for _, schedule := range c.recurrences {
		if schedule.overlaps(start, end) {
			return false
		}
	}
	return true
}

type scored struct {
	car      *car
	distance float64
}

// searchCells lists the grid cells the circle touches, nearest first, with
// each cell's minimum possible distance so the caller can stop early.
type searchCell struct {
	cars    map[string]*car
	minDist float64
}

func (m *model) searchCells(lat, lng, rangeMeters float64) []searchCell {
	lngScale := math.Cos(lat * math.Pi / 180)
	latSpan := rangeMeters / 111320
	lngSpan := rangeMeters / (111320 * lngScale)
	lowY := int32(math.Floor((lat - latSpan) / cellDegrees))
	highY := int32(math.Floor((lat + latSpan) / cellDegrees))
	lowX := int32(math.Floor((lng - lngSpan) / cellDegrees))
	highX := int32(math.Floor((lng + lngSpan) / cellDegrees))

	var cells []searchCell
	for y := lowY; y <= highY; y++ {
		for x := lowX; x <= highX; x++ {
			bucket, ok := m.cells[int64(y)<<32|int64(uint32(x))]
			if !ok {
				continue
			}
			// Distance from the search point to the nearest point of the
			// cell's rectangle, in the same flat approximation as the SQL.
			latDelta := axisGap(lat, float64(y)*cellDegrees, float64(y+1)*cellDegrees)
			lngDelta := axisGap(lng, float64(x)*cellDegrees, float64(x+1)*cellDegrees) * lngScale
			minDist := 111320 * math.Sqrt(latDelta*latDelta+lngDelta*lngDelta)
			if minDist <= rangeMeters {
				cells = append(cells, searchCell{cars: bucket, minDist: minDist})
			}
		}
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].minDist < cells[j].minDist })
	return cells
}

func axisGap(value, low, high float64) float64 {
	switch {
	case value < low:
		return low - value
	case value > high:
		return value - high
	default:
		return 0
	}
}

// Availability answers the same contract as the store's SQL, from RAM. The
// distance sort visits cells nearest-first and stops once no unvisited cell
// can beat the page, the in-memory version of the KNN index walk. The price
// sort must consider the whole circle, a few milliseconds at worst-case
// density, and unlike the SQL's bounded-candidate version it is exact.
func (fleet *Fleet) Availability(_ context.Context, params store.AvailabilityParams) ([]store.AvailableCar, error) {
	now := time.Now()
	need := params.Limit + params.Offset
	byPrice := params.Sort == "price"

	fleet.mu.RLock()
	defer fleet.mu.RUnlock()

	cells := fleet.model.searchCells(params.Lat, params.Lng, params.RangeMeters)
	var results []scored
	for _, cell := range cells {
		if !byPrice && len(results) >= need && cell.minDist > results[need-1].distance {
			break
		}
		for _, candidate := range cell.cars {
			distance := store.DistanceMeters(params.Lat, params.Lng, candidate.lat, candidate.lng)
			if distance > params.RangeMeters || !candidate.available(params.Start, params.End, now) {
				continue
			}
			results = append(results, scored{car: candidate, distance: distance})
		}
		if !byPrice && len(results) >= need {
			sortByDistance(results, params, now)
		}
	}

	if byPrice {
		sortByPrice(results, params, now)
	} else {
		sortByDistance(results, params, now)
	}
	if params.Offset >= len(results) {
		return nil, nil
	}
	results = results[params.Offset:]
	if len(results) > params.Limit {
		results = results[:params.Limit]
	}

	tripHours := params.End.Sub(params.Start).Hours()
	page := make([]store.AvailableCar, 0, len(results))
	for _, result := range results {
		page = append(page, store.AvailableCar{
			Car: store.Car{
				ID: result.car.id, Model: result.car.model, ModelYear: result.car.modelYear,
				Lat: result.car.lat, Lng: result.car.lng,
				PricePerHour: result.car.pricePerHour, IsListed: true,
			},
			TripPrice:      int(math.Round(float64(result.car.pricePerHour) * tripHours)),
			DistanceMeters: result.distance,
		})
	}
	return page, nil
}

func sortByDistance(results []scored, params store.AvailabilityParams, _ time.Time) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].distance != results[j].distance {
			return results[i].distance < results[j].distance
		}
		if results[i].car.pricePerHour != results[j].car.pricePerHour {
			return results[i].car.pricePerHour < results[j].car.pricePerHour
		}
		return results[i].car.id < results[j].car.id
	})
}

func sortByPrice(results []scored, params store.AvailabilityParams, _ time.Time) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].car.pricePerHour != results[j].car.pricePerHour {
			return results[i].car.pricePerHour < results[j].car.pricePerHour
		}
		if results[i].distance != results[j].distance {
			return results[i].distance < results[j].distance
		}
		return results[i].car.id < results[j].car.id
	})
}

// AvailabilityCount counts matching cars, stopping at params.Cap.
func (fleet *Fleet) AvailabilityCount(_ context.Context, params store.AvailabilityCountParams) (int, error) {
	now := time.Now()

	fleet.mu.RLock()
	defer fleet.mu.RUnlock()

	total := 0
	for _, cell := range fleet.model.searchCells(params.Lat, params.Lng, params.RangeMeters) {
		for _, candidate := range cell.cars {
			if store.DistanceMeters(params.Lat, params.Lng, candidate.lat, candidate.lng) > params.RangeMeters {
				continue
			}
			if !candidate.available(params.Start, params.End, now) {
				continue
			}
			total++
			if total >= params.Cap {
				return total, nil
			}
		}
	}
	return total, nil
}

// Cars reports the number of cars held, for the metrics gauge.
func (fleet *Fleet) Cars() int {
	fleet.mu.RLock()
	defer fleet.mu.RUnlock()
	return len(fleet.model.cars)
}
