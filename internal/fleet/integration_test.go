// Integration tests against a real Postgres: the schema's triggers write
// cars.fleet_log, the fleet follows it, and every kind of change converges
// into search results. Run scripts/dev_db.sh and export
// CARSHARE_TEST_DATABASE_URL to enable them.
package fleet_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"carshare/internal/fleet"
	"carshare/internal/store"
	"carshare/internal/testutil/pgtest"
)

const searchLat, searchLng = 40.75, -73.99

// waitFor pokes the fleet awake and polls until the condition holds. The
// poll loop pulls on every poke, so convergence is bounded by round trips,
// not by the 250ms tick.
func waitFor(t *testing.T, wake fleet.Wake, condition func() bool, described string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		wake.Poke()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", described)
}

func TestFleetConverges(t *testing.T) {
	pool := pgtest.MustPool(t)
	dataStore := store.NewWithPool(pool)
	ctx := context.Background()

	owner, err := dataStore.UpsertIdentity(ctx, "google", fmt.Sprintf("sub-%d", time.Now().UnixNano()), "owner@example.com", "Owner", "")
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	renter, err := dataStore.UpsertIdentity(ctx, "google", fmt.Sprintf("sub-r-%d", time.Now().UnixNano()), "renter@example.com", "Renter", "")
	if err != nil {
		t.Fatalf("renter: %v", err)
	}

	// Snapshot path: a car listed before the fleet starts is visible at boot.
	before, err := dataStore.CreateCar(ctx, owner.ID, store.NewCar{Lat: searchLat, Lng: searchLng, PricePerHour: 1000, Model: "Before"})
	if err != nil {
		t.Fatalf("create car: %v", err)
	}

	wake := make(fleet.Wake, 1)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	searchFleet, err := fleet.Start(runCtx, pool, wake)
	if err != nil {
		t.Fatalf("fleet start: %v", err)
	}

	start := time.Now().Add(72 * time.Hour).Truncate(time.Hour)
	finds := func(carID string) func() bool {
		return func() bool {
			results, err := searchFleet.Availability(ctx, store.AvailabilityParams{
				Lat: searchLat, Lng: searchLng, RangeMeters: 10_000,
				Start: start, End: start.Add(2 * time.Hour), Sort: "distance", Limit: 100,
			})
			if err != nil {
				t.Fatalf("availability: %v", err)
			}
			for _, result := range results {
				if result.ID == carID {
					return true
				}
			}
			return false
		}
	}
	missing := func(carID string) func() bool {
		found := finds(carID)
		return func() bool { return !found() }
	}

	if !finds(before.ID)() {
		t.Fatal("snapshot should include the pre-existing car")
	}

	// Feed path: a car listed after boot arrives through fleet_log.
	after, err := dataStore.CreateCar(ctx, owner.ID, store.NewCar{Lat: searchLat + 0.001, Lng: searchLng, PricePerHour: 2000, Model: "After"})
	if err != nil {
		t.Fatalf("create car: %v", err)
	}
	waitFor(t, wake, finds(after.ID), "new car to reach the fleet")

	// A booking hides the car for its window, cancelling brings it back.
	booking, err := dataStore.OrderCar(ctx, store.OrderParams{
		CarID: after.ID, BookerID: renter.ID, Kind: store.KindRental,
		Start: start, End: start.Add(2 * time.Hour), MaxPrice: 4000,
	})
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	waitFor(t, wake, missing(after.ID), "booked car to leave the fleet")
	if err := dataStore.CancelReservation(ctx, booking.ID, renter.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, wake, finds(after.ID), "cancelled booking to free the car")

	// Unlisting removes the car from search.
	hidden := false
	if _, err := dataStore.UpdateCar(ctx, owner.ID, before.ID, store.CarPatch{IsListed: &hidden}); err != nil {
		t.Fatalf("unlist: %v", err)
	}
	waitFor(t, wake, missing(before.ID), "unlisted car to leave the fleet")

	// An owner schedule blocks its occurrences.
	if _, err := dataStore.CreateSchedule(ctx, store.ScheduleParams{
		CarID: after.ID, OwnerID: owner.ID,
		Start: start.Add(-7 * 24 * time.Hour), End: start.Add(-7*24*time.Hour + 3*time.Hour),
		Period: "weekly", Timezone: "UTC",
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	waitFor(t, wake, missing(after.ID), "scheduled window to block the car")
}
