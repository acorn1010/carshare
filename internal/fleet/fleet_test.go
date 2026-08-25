package fleet

import (
	"context"
	"testing"
	"time"

	"carshare/internal/store"
)

func timeAt(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02 15:04-07", value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

// TestRecurrenceOverlapsParity runs the Go port against the same cases the
// SQL is pinned by in internal/store's TestRecurrenceOverlapsSQL, so the
// fleet and the booking path can never disagree about a schedule.
func TestRecurrenceOverlapsParity(t *testing.T) {
	cases := []struct {
		name                   string
		firstStart, firstEnd   string
		period, timezone       string
		windowStart, windowEnd string
		want                   bool
	}{
		{"weekly hit", "2026-01-07 13:00-08", "2026-01-07 15:00-08", "weekly", "America/Los_Angeles", "2026-08-26 20:30+00", "2026-08-26 21:30+00", true},
		{"weekly miss next day", "2026-01-07 13:00-08", "2026-01-07 15:00-08", "weekly", "America/Los_Angeles", "2026-08-27 20:30+00", "2026-08-27 21:30+00", false},
		{"dst keeps wall clock", "2026-01-07 13:00-08", "2026-01-07 15:00-08", "weekly", "America/Los_Angeles", "2026-08-26 12:00-07", "2026-08-26 13:30-07", true},
		{"dst no false hit", "2026-01-07 13:00-08", "2026-01-07 15:00-08", "weekly", "America/Los_Angeles", "2026-08-26 11:00-07", "2026-08-26 12:59-07", false},
		{"monthly clamps jan31", "2026-01-31 10:00+00", "2026-01-31 12:00+00", "monthly", "UTC", "2026-02-28 11:00+00", "2026-02-28 11:30+00", true},
		{"yearly hit", "2026-06-01 10:00+00", "2026-06-01 12:00+00", "yearly", "UTC", "2028-06-01 11:00+00", "2028-06-01 11:30+00", true},
		{"before first occurrence", "2026-06-01 10:00+00", "2026-06-01 12:00+00", "weekly", "UTC", "2026-05-01 10:00+00", "2026-05-01 12:00+00", false},
		{"long window hits", "2026-01-07 13:00-08", "2026-01-07 15:00-08", "weekly", "America/Los_Angeles", "2026-08-20 00:00+00", "2026-09-05 00:00+00", true},
	}
	for _, testCase := range cases {
		location, err := time.LoadLocation(testCase.timezone)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		schedule := recurrence{
			firstStart: timeAt(t, testCase.firstStart), firstEnd: timeAt(t, testCase.firstEnd),
			period: testCase.period, location: location,
		}
		got := schedule.overlaps(timeAt(t, testCase.windowStart), timeAt(t, testCase.windowEnd))
		if got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}

// carEntry builds a car change entry at a position near the test's search
// point, priced as given.
func carEntry(id string, lat, lng float64, price int) entry {
	return entry{Table: "car", Op: "INSERT", ID: id, Model: "Test",
		Lat: lat, Lng: lng, PricePerHour: price, IsListed: true}
}

func testFleet(entries ...entry) *Fleet {
	fleet := &Fleet{model: newModel()}
	now := time.Now()
	for _, change := range entries {
		fleet.model.apply(change, now)
	}
	return fleet
}

func searchIDs(t *testing.T, fleet *Fleet, sortMode string) []string {
	t.Helper()
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	results, err := fleet.Availability(context.Background(), store.AvailabilityParams{
		Lat: 40.75, Lng: -73.99, RangeMeters: 10_000,
		Start: start, End: start.Add(2 * time.Hour),
		Sort: sortMode, Limit: 100,
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

func TestSearchOrdersAndFilters(t *testing.T) {
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	end := start.Add(2 * time.Hour)
	expired := time.Now().Add(-time.Hour)

	fleet := testFleet(
		// Cars at increasing distance north of the search point. The pricey
		// one is nearest, the cheap one farthest, cells apart so the ring
		// walk is exercised.
		carEntry("near-pricey", 40.751, -73.99, 3000),
		carEntry("mid", 40.78, -73.99, 2000),
		carEntry("far-cheap", 40.82, -73.99, 500),
		carEntry("outside-radius", 40.95, -73.99, 100),
		entry{Table: "car", Op: "INSERT", ID: "unlisted", Lat: 40.752, Lng: -73.99, PricePerHour: 100, IsListed: false},
		carEntry("booked", 40.753, -73.99, 100),
		entry{Table: "reservation", Op: "INSERT", ID: "r1", CarID: "booked", Status: "confirmed", Start: &start, End: &end},
		carEntry("expired-hold", 40.754, -73.99, 4000),
		entry{Table: "reservation", Op: "INSERT", ID: "r2", CarID: "expired-hold", Status: "confirmed", Start: &start, End: &end, HoldExpiresAt: &expired},
	)

	byDistance := searchIDs(t, fleet, "distance")
	wantDistance := []string{"near-pricey", "expired-hold", "mid", "far-cheap"}
	if len(byDistance) != len(wantDistance) {
		t.Fatalf("distance sort: got %v want %v", byDistance, wantDistance)
	}
	for i := range wantDistance {
		if byDistance[i] != wantDistance[i] {
			t.Fatalf("distance sort: got %v want %v", byDistance, wantDistance)
		}
	}

	byPrice := searchIDs(t, fleet, "price")
	wantPrice := []string{"far-cheap", "mid", "near-pricey", "expired-hold"}
	for i := range wantPrice {
		if byPrice[i] != wantPrice[i] {
			t.Fatalf("price sort: got %v want %v", byPrice, wantPrice)
		}
	}

	// Cancelling the booking brings the car back.
	fleet.model.apply(entry{Table: "reservation", Op: "UPDATE", ID: "r1", CarID: "booked", Status: "cancelled", Start: &start, End: &end}, time.Now())
	if ids := searchIDs(t, fleet, "distance"); len(ids) != 5 || ids[1] != "booked" {
		t.Fatalf("after cancel: got %v", ids)
	}

	total, err := fleet.AvailabilityCount(context.Background(), store.AvailabilityCountParams{
		Lat: 40.75, Lng: -73.99, RangeMeters: 10_000,
		Start: start, End: end, Cap: 3,
	})
	if err != nil || total != 3 {
		t.Fatalf("count capped: got %d, %v", total, err)
	}
}

func TestSearchPaginates(t *testing.T) {
	entries := make([]entry, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, carEntry(
			string(rune('a'+i%26))+string(rune('0'+i/26)),
			40.75+float64(i)*0.001, -73.99, 1000+i))
	}
	fleet := testFleet(entries...)
	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	params := store.AvailabilityParams{
		Lat: 40.75, Lng: -73.99, RangeMeters: 10_000,
		Start: start, End: start.Add(time.Hour), Sort: "distance", Limit: 10,
	}
	first, err := fleet.Availability(context.Background(), params)
	if err != nil || len(first) != 10 {
		t.Fatalf("page 0: %d results, %v", len(first), err)
	}
	params.Offset = 10
	second, err := fleet.Availability(context.Background(), params)
	if err != nil || len(second) != 10 {
		t.Fatalf("page 1: %d results, %v", len(second), err)
	}
	if first[9].DistanceMeters > second[0].DistanceMeters {
		t.Fatalf("pages out of order: %f then %f", first[9].DistanceMeters, second[0].DistanceMeters)
	}
}
