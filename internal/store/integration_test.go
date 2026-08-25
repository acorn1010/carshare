// Integration tests against a real Postgres. They exist to prove the
// concurrency story: the exclusion constraint, hold stealing, idempotent
// replay, and the recurrence SQL. Run scripts/dev_db.sh and export
// CARSHARE_TEST_DATABASE_URL to enable them.
package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"carshare/internal/store"
	"carshare/internal/testutil/pgtest"

	"github.com/jackc/pgx/v5/pgconn"
)

var userCounter atomic.Int64

func newStore(t *testing.T) *store.Store {
	t.Helper()
	return store.NewWithPool(pgtest.MustPool(t))
}

func newUser(t *testing.T, dataStore *store.Store, name string) store.User {
	t.Helper()
	sub := fmt.Sprintf("sub-%d-%d", userCounter.Add(1), time.Now().UnixNano())
	user, err := dataStore.UpsertIdentity(context.Background(), "google", sub, name+"@example.com", name, "")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func newCar(t *testing.T, dataStore *store.Store, ownerID string, lat, lng float64, pricePerHour int) store.Car {
	t.Helper()
	car, err := dataStore.CreateCar(context.Background(), ownerID, store.NewCar{
		Lat: lat, Lng: lng, PricePerHour: pricePerHour, Model: "Test Car",
	})
	if err != nil {
		t.Fatalf("create car: %v", err)
	}
	return car
}

// baseTime is far enough out that the 24 hour cancel rule never interferes
// unless a test wants it to.
func baseTime() time.Time {
	return time.Now().Add(72 * time.Hour).Truncate(time.Hour)
}

func rentalOrder(car store.Car, bookerID string, start time.Time, hours int) store.OrderParams {
	return store.OrderParams{
		CarID:    car.ID,
		BookerID: bookerID,
		Kind:     store.KindRental,
		Start:    start,
		End:      start.Add(time.Duration(hours) * time.Hour),
		MaxPrice: car.PricePerHour * hours,
	}
}

func TestOrderCarConcurrentOneWins(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1500)
	start := baseTime()

	const racers = 8
	var wins, conflicts atomic.Int64
	var waitGroup sync.WaitGroup
	for i := 0; i < racers; i++ {
		renter := newUser(t, dataStore, fmt.Sprintf("renter%d", i))
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, start, 2))
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	waitGroup.Wait()

	if wins.Load() != 1 || conflicts.Load() != racers-1 {
		t.Fatalf("want 1 win and %d conflicts, got %d wins and %d conflicts", racers-1, wins.Load(), conflicts.Load())
	}
	pairs, err := dataStore.CountDoubleBookedPairs(ctx)
	if err != nil {
		t.Fatalf("invariant: %v", err)
	}
	if pairs != 0 {
		t.Fatalf("double booked pairs = %d, want 0", pairs)
	}
}

func TestContiguousWindowsBothSucceed(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renterA := newUser(t, dataStore, "renterA")
	renterB := newUser(t, dataStore, "renterB")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renterA.ID, start, 1)); err != nil {
		t.Fatalf("first booking: %v", err)
	}
	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renterB.ID, start.Add(time.Hour), 1)); err != nil {
		t.Fatalf("contiguous booking should succeed: %v", err)
	}
}

// rawReservationInsert writes a reservation with plain SQL, past every
// application check, so tests can hit the schema's own defenses.
const rawReservationInsert = `
	INSERT INTO cars.reservations (car_id, booker_id, kind, during, price, status)
	VALUES ($1, $2, 'rental', tstzrange($3, $4), 1000, $5)`

// TestExclusionConstraintBlocksRawInsert proves the schema alone rejects an
// overlapping confirmed reservation. The CarshareDoubleBookingDetected alert
// treats a double booking as impossible because of this constraint, so
// removing or weakening it must fail this test, not just the alert.
func TestExclusionConstraintBlocksRawInsert(t *testing.T) {
	pool := pgtest.MustPool(t)
	dataStore := store.NewWithPool(pool)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	if _, err := pool.Exec(ctx, rawReservationInsert,
		car.ID, renter.ID, start, start.Add(2*time.Hour), "confirmed"); err != nil {
		t.Fatalf("first raw insert: %v", err)
	}

	_, err := pool.Exec(ctx, rawReservationInsert,
		car.ID, renter.ID, start.Add(time.Hour), start.Add(3*time.Hour), "confirmed")
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23P01" {
		t.Fatalf("overlapping raw insert: want exclusion violation 23P01, got %v", err)
	}
	if pgErr.ConstraintName != "cars_reservations_no_overlap" {
		t.Fatalf("want constraint cars_reservations_no_overlap, got %q", pgErr.ConstraintName)
	}

	// The WHERE clause is part of the guarantee: a cancelled overlap must
	// stay legal, or cancelling a reservation would have to delete the row.
	if _, err := pool.Exec(ctx, rawReservationInsert,
		car.ID, renter.ID, start.Add(time.Hour), start.Add(3*time.Hour), "cancelled"); err != nil {
		t.Fatalf("cancelled overlap should insert: %v", err)
	}
}

// TestDoubleBookedPairsDetectorSeesDamage drops the constraint the way a bad
// migration would and checks the invariant query actually counts the overlap
// that then slips in. This query feeds the critical alert; if it goes blind
// the alert can never fire. pgtest rebuilds the schema for every test, so the
// damage does not leak.
func TestDoubleBookedPairsDetectorSeesDamage(t *testing.T) {
	pool := pgtest.MustPool(t)
	dataStore := store.NewWithPool(pool)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	if _, err := pool.Exec(ctx,
		`ALTER TABLE cars.reservations DROP CONSTRAINT cars_reservations_no_overlap`); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(ctx, rawReservationInsert,
			car.ID, renter.ID, start, start.Add(2*time.Hour), "confirmed"); err != nil {
			t.Fatalf("raw insert %d: %v", i, err)
		}
	}

	pairs, err := dataStore.CountDoubleBookedPairs(ctx)
	if err != nil {
		t.Fatalf("invariant: %v", err)
	}
	if pairs != 1 {
		t.Fatalf("double booked pairs = %d, want 1", pairs)
	}
}

func TestExpiredHoldStolenByExactlyOne(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	holder := newUser(t, dataStore, "holder")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	expiredHold := rentalOrder(car, holder.ID, start, 2)
	expiredHold.Kind = store.KindRentalHold
	expiredHold.HoldTTL = -time.Second
	if _, err := dataStore.OrderCar(ctx, expiredHold); err != nil {
		t.Fatalf("create expired hold: %v", err)
	}

	var wins, conflicts atomic.Int64
	var waitGroup sync.WaitGroup
	for i := 0; i < 2; i++ {
		renter := newUser(t, dataStore, fmt.Sprintf("stealer%d", i))
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, start, 2))
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, store.ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	waitGroup.Wait()
	if wins.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("want exactly one stealer to win, got %d wins %d conflicts", wins.Load(), conflicts.Load())
	}
}

func TestLiveHoldBlocksAndConfirms(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	holder := newUser(t, dataStore, "holder")
	other := newUser(t, dataStore, "other")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	holdOrder := rentalOrder(car, holder.ID, start, 2)
	holdOrder.Kind = store.KindRentalHold
	holdOrder.HoldTTL = 10 * time.Minute
	hold, err := dataStore.OrderCar(ctx, holdOrder)
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	if hold.HoldExpiresAt == nil {
		t.Fatal("hold has no expiry")
	}

	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, other.ID, start, 2)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("live hold should block, got %v", err)
	}

	if _, err := dataStore.ConfirmHold(ctx, hold.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign confirm should fail, got %v", err)
	}
	confirmed, err := dataStore.ConfirmHold(ctx, hold.ID, holder.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Kind != store.KindRental || confirmed.HoldExpiresAt != nil {
		t.Fatalf("confirm left kind=%s expiry=%v", confirmed.Kind, confirmed.HoldExpiresAt)
	}
	if _, err := dataStore.ConfirmHold(ctx, hold.ID, holder.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double confirm should fail, got %v", err)
	}
}

func TestPriceChangedRejected(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	newPrice := 2000
	if _, err := dataStore.UpdateCar(ctx, owner.ID, car.ID, store.CarPatch{PricePerHour: &newPrice}); err != nil {
		t.Fatalf("raise price: %v", err)
	}

	// The renter still quotes the old price and must be rejected.
	stale := rentalOrder(car, renter.ID, start, 2)
	if _, err := dataStore.OrderCar(ctx, stale); !errors.Is(err, store.ErrPriceChanged) {
		t.Fatalf("want ErrPriceChanged, got %v", err)
	}

	fresh := rentalOrder(car, renter.ID, start, 2)
	fresh.MaxPrice = newPrice * 2
	booked, err := dataStore.OrderCar(ctx, fresh)
	if err != nil {
		t.Fatalf("booking at current price: %v", err)
	}
	if booked.Price == nil || *booked.Price != newPrice*2 {
		t.Fatalf("frozen price = %v, want %d", booked.Price, newPrice*2)
	}
}

func TestIdempotencyKeyReplay(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	order := rentalOrder(car, renter.ID, start, 2)
	order.IdempotencyKey = "retry-me"
	first, err := dataStore.OrderCar(ctx, order)
	if err != nil {
		t.Fatalf("first order: %v", err)
	}
	replay, err := dataStore.OrderCar(ctx, order)
	if err != nil {
		t.Fatalf("replay should return the original, got %v", err)
	}
	if replay.ID != first.ID {
		t.Fatalf("replay returned %s, want %s", replay.ID, first.ID)
	}
}

func TestScheduleBlocksBookingAndBookingBeatsSchedule(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	first := baseTime()

	schedule, err := dataStore.CreateSchedule(ctx, store.ScheduleParams{
		CarID: car.ID, OwnerID: owner.ID,
		Start: first, End: first.Add(2 * time.Hour),
		Period: "weekly", Timezone: "UTC",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// Three weeks out, same slot: blocked by the recurrence.
	occurrence := first.Add(3 * 7 * 24 * time.Hour)
	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, occurrence, 2)); !errors.Is(err, store.ErrScheduleConflict) {
		t.Fatalf("want ErrScheduleConflict, got %v", err)
	}
	// An hour after the occurrence ends: free.
	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, occurrence.Add(3*time.Hour), 2)); err != nil {
		t.Fatalf("outside occurrence should book: %v", err)
	}

	// A reservation made before a schedule exists always stands. The owner
	// scheduling over it succeeds, they just miss that occurrence.
	futureSlot := first.Add(5 * 7 * 24 * time.Hour).Add(6 * time.Hour)
	booked, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, futureSlot, 2))
	if err != nil {
		t.Fatalf("booking: %v", err)
	}
	if _, err := dataStore.CreateSchedule(ctx, store.ScheduleParams{
		CarID: car.ID, OwnerID: owner.ID,
		Start: futureSlot, End: futureSlot.Add(2 * time.Hour),
		Period: "weekly", Timezone: "UTC",
	}); err != nil {
		t.Fatalf("schedule over existing booking must succeed: %v", err)
	}

	// Cancelling the first schedule frees its occurrences.
	if err := dataStore.DeactivateSchedule(ctx, schedule.ID, renter.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner deactivate should fail, got %v", err)
	}
	if err := dataStore.DeactivateSchedule(ctx, schedule.ID, owner.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, occurrence, 2)); err != nil {
		t.Fatalf("occurrence should be free after deactivate: %v", err)
	}
	_ = booked
}

func TestOwnerBooking(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	stranger := newUser(t, dataStore, "stranger")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	// Owners can block their own car even when it is unlisted, price is not
	// checked, and no price is frozen.
	unlisted := false
	if _, err := dataStore.UpdateCar(ctx, owner.ID, car.ID, store.CarPatch{IsListed: &unlisted}); err != nil {
		t.Fatalf("unlist: %v", err)
	}
	ownerHold := store.OrderParams{
		CarID: car.ID, BookerID: owner.ID, Kind: store.KindOwner,
		Start: start, End: start.Add(4 * time.Hour),
	}
	reservation, err := dataStore.OrderCar(ctx, ownerHold)
	if err != nil {
		t.Fatalf("owner hold: %v", err)
	}
	if reservation.Price != nil {
		t.Fatalf("owner hold has price %v, want none", *reservation.Price)
	}

	strangerHold := ownerHold
	strangerHold.BookerID = stranger.ID
	if _, err := dataStore.OrderCar(ctx, strangerHold); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger owner-kind should fail with ErrNotFound, got %v", err)
	}
}

func TestCancelRules(t *testing.T) {
	pool := pgtest.MustPool(t)
	dataStore := store.NewWithPool(pool)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	other := newUser(t, dataStore, "other")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)

	farOut := rentalOrder(car, renter.ID, baseTime(), 2)
	booked, err := dataStore.OrderCar(ctx, farOut)
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if err := dataStore.CancelReservation(ctx, booked.ID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign cancel should fail, got %v", err)
	}
	if err := dataStore.CancelReservation(ctx, booked.ID, renter.ID); err != nil {
		t.Fatalf("cancel >24h out: %v", err)
	}
	// The slot is free again.
	if _, err := dataStore.OrderCar(ctx, farOut); err != nil {
		t.Fatalf("rebooking a cancelled slot: %v", err)
	}

	// Booked inside the 24 hour window: cancelling right away rides the one
	// hour booking grace.
	soon := rentalOrder(car, renter.ID, time.Now().Add(2*time.Hour).Truncate(time.Minute), 1)
	graced, err := dataStore.OrderCar(ctx, soon)
	if err != nil {
		t.Fatalf("book soon: %v", err)
	}
	if err := dataStore.CancelReservation(ctx, graced.ID, renter.ID); err != nil {
		t.Fatalf("grace cancel should succeed, got %v", err)
	}

	// Same booking with the grace hour spent: too late.
	lastMinute, err := dataStore.OrderCar(ctx, soon)
	if err != nil {
		t.Fatalf("rebook soon: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE cars.reservations SET created_at = now() - interval '2 hours' WHERE id = $1`,
		lastMinute.ID); err != nil {
		t.Fatalf("backdate booking: %v", err)
	}
	if err := dataStore.CancelReservation(ctx, lastMinute.ID, renter.ID); !errors.Is(err, store.ErrTooLateToCancel) {
		t.Fatalf("want ErrTooLateToCancel, got %v", err)
	}

	// Holds cancel any time, even inside 24 hours.
	holdSoon := rentalOrder(car, renter.ID, time.Now().Add(4*time.Hour).Truncate(time.Minute), 1)
	holdSoon.Kind = store.KindRentalHold
	holdSoon.HoldTTL = 10 * time.Minute
	hold, err := dataStore.OrderCar(ctx, holdSoon)
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if err := dataStore.CancelReservation(ctx, hold.ID, renter.ID); err != nil {
		t.Fatalf("cancel hold: %v", err)
	}
}

// TestFractionalHourPricing pins the float cast in the trip price SQL.
// Without the explicit ::float8, Postgres inferred the hours parameter as an
// integer and floored it, which billed 90 minutes as 1 hour and 30 minutes as
// free.
func TestFractionalHourPricing(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 700)
	start := baseTime()

	results, err := dataStore.Availability(ctx, store.AvailabilityParams{
		Lat: 37.77, Lng: -122.42, RangeMeters: 5000,
		Start: start, End: start.Add(30 * time.Minute), Limit: 10,
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if len(results) != 1 || results[0].TripPrice != 350 {
		t.Fatalf("30 minutes at 700/h priced %v, want 350", results)
	}

	booked, err := dataStore.OrderCar(ctx, store.OrderParams{
		CarID: car.ID, BookerID: renter.ID, Kind: store.KindRental,
		Start: start, End: start.Add(90 * time.Minute), MaxPrice: 1050,
	})
	if err != nil {
		t.Fatalf("book 90 minutes: %v", err)
	}
	if booked.Price == nil || *booked.Price != 1050 {
		t.Fatalf("90 minutes at 700/h froze %v, want 1050", booked.Price)
	}
}

func TestAvailabilitySortingAndFilters(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	searchLat, searchLng := 37.7700, -122.4200
	start := baseTime()
	window := store.AvailabilityParams{
		Lat: searchLat, Lng: searchLng, RangeMeters: 10_000,
		Start: start, End: start.Add(2 * time.Hour),
		Limit: 100,
	}

	cheapFar := newCar(t, dataStore, owner.ID, searchLat+0.03, searchLng, 500)
	cheapNear := newCar(t, dataStore, owner.ID, searchLat+0.001, searchLng, 500)
	pricey := newCar(t, dataStore, owner.ID, searchLat, searchLng+0.001, 2000)
	booked := newCar(t, dataStore, owner.ID, searchLat, searchLng-0.001, 300)
	held := newCar(t, dataStore, owner.ID, searchLat-0.001, searchLng, 300)
	expiredHeld := newCar(t, dataStore, owner.ID, searchLat-0.002, searchLng, 4000)
	scheduled := newCar(t, dataStore, owner.ID, searchLat+0.002, searchLng, 350)
	unlisted := newCar(t, dataStore, owner.ID, searchLat, searchLng, 100)
	farAway := newCar(t, dataStore, owner.ID, searchLat+1.0, searchLng, 100)

	if _, err := dataStore.OrderCar(ctx, rentalOrder(booked, renter.ID, start, 2)); err != nil {
		t.Fatalf("seed booking: %v", err)
	}
	holdOrder := rentalOrder(held, renter.ID, start, 2)
	holdOrder.Kind = store.KindRentalHold
	holdOrder.HoldTTL = 10 * time.Minute
	if _, err := dataStore.OrderCar(ctx, holdOrder); err != nil {
		t.Fatalf("seed hold: %v", err)
	}
	deadHold := rentalOrder(expiredHeld, renter.ID, start, 2)
	deadHold.Kind = store.KindRentalHold
	deadHold.HoldTTL = -time.Second
	if _, err := dataStore.OrderCar(ctx, deadHold); err != nil {
		t.Fatalf("seed expired hold: %v", err)
	}
	if _, err := dataStore.CreateSchedule(ctx, store.ScheduleParams{
		CarID: scheduled.ID, OwnerID: owner.ID,
		Start: start.Add(-7 * 24 * time.Hour), End: start.Add(-7*24*time.Hour + time.Hour),
		Period: "weekly", Timezone: "UTC",
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	hidden := false
	if _, err := dataStore.UpdateCar(ctx, owner.ID, unlisted.ID, store.CarPatch{IsListed: &hidden}); err != nil {
		t.Fatalf("unlist: %v", err)
	}

	results, err := dataStore.Availability(ctx, window)
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	var gotIDs []string
	for _, result := range results {
		gotIDs = append(gotIDs, result.ID)
	}
	// scheduled's weekly occurrence covers only the first hour of the window,
	// so it is out. booked, held, unlisted, farAway are out. expiredHeld is in
	// because its hold is dead. Order: cheap first, near beats far on the tie.
	wantIDs := []string{cheapNear.ID, cheapFar.ID, pricey.ID, expiredHeld.ID}
	if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
		t.Fatalf("got %v\nwant %v", gotIDs, wantIDs)
	}
	if results[0].TripPrice != 1000 {
		t.Fatalf("cheap trip price = %d, want 1000", results[0].TripPrice)
	}
	if results[0].DistanceMeters <= 0 || results[0].DistanceMeters > 200 {
		t.Fatalf("near car distance = %.0f m, want ~111 m", results[0].DistanceMeters)
	}
	_ = farAway

	// Pagination: page size 2.
	window.Limit, window.Offset = 2, 2
	page2, err := dataStore.Availability(ctx, window)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != pricey.ID || page2[1].ID != expiredHeld.ID {
		t.Fatalf("page 2 mismatch: %v", page2)
	}
}

func TestSessions(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	user := newUser(t, dataStore, "sess")

	if err := dataStore.CreateSession(ctx, user.ID, "livehash", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	got, err := dataStore.UserBySessionToken(ctx, "livehash")
	if err != nil || got.ID != user.ID {
		t.Fatalf("lookup = %v, %v", got.ID, err)
	}

	if err := dataStore.CreateSession(ctx, user.ID, "deadhash", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if _, err := dataStore.UserBySessionToken(ctx, "deadhash"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session should miss, got %v", err)
	}

	if err := dataStore.DeleteSession(ctx, "livehash"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := dataStore.UserBySessionToken(ctx, "livehash"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted session should miss, got %v", err)
	}
}

func TestCarCalendarOwnerOnly(t *testing.T) {
	dataStore := newStore(t)
	ctx := context.Background()
	owner := newUser(t, dataStore, "owner")
	renter := newUser(t, dataStore, "renter")
	car := newCar(t, dataStore, owner.ID, 37.77, -122.42, 1000)
	start := baseTime()

	if _, err := dataStore.OrderCar(ctx, rentalOrder(car, renter.ID, start, 2)); err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := dataStore.CreateSchedule(ctx, store.ScheduleParams{
		CarID: car.ID, OwnerID: owner.ID,
		Start: start.Add(24 * time.Hour), End: start.Add(26 * time.Hour),
		Period: "weekly", Timezone: "UTC",
	}); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	calendar, err := dataStore.CarCalendar(ctx, car.ID, owner.ID, start.Add(-time.Hour), start.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("calendar: %v", err)
	}
	if len(calendar.Reservations) != 1 || len(calendar.Recurrences) != 1 {
		t.Fatalf("calendar = %d reservations %d recurrences, want 1 and 1", len(calendar.Reservations), len(calendar.Recurrences))
	}
	if _, err := dataStore.CarCalendar(ctx, car.ID, renter.ID, start, start.Add(time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner calendar should fail, got %v", err)
	}
}

// TestRecurrenceOverlapsSQL pins the SQL function's edge cases: DST keeps
// wall-clock time, monthly clamps Jan 31 to Feb 28, and long windows spanning
// several periods still hit.
func TestRecurrenceOverlapsSQL(t *testing.T) {
	pool := pgtest.MustPool(t)
	ctx := context.Background()

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
		var got bool
		err := pool.QueryRow(ctx, `
			SELECT cars.recurrence_overlaps(
				tstzrange($1::timestamptz, $2::timestamptz), $3, $4,
				tstzrange($5::timestamptz, $6::timestamptz))`,
			testCase.firstStart, testCase.firstEnd, testCase.period, testCase.timezone,
			testCase.windowStart, testCase.windowEnd).Scan(&got)
		if err != nil {
			t.Fatalf("%s: %v", testCase.name, err)
		}
		if got != testCase.want {
			t.Errorf("%s: got %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
