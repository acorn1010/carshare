// Package store owns every SQL statement in the service. Handlers talk to the
// DataStore interface and never see pgx, which keeps the concurrency-critical
// SQL in one reviewable place and lets tests swap in memstore.
//
// The core correctness idea: the cars_reservations_no_overlap exclusion
// constraint is the single source of truth against double-booking. Every write
// here is either a single conditional statement or a short transaction that
// ends in an insert the constraint can reject, so there is no check-then-act
// window anywhere.
package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"carshare/internal/metrics"
)

// Sentinel errors handlers translate into HTTP statuses.
var (
	// ErrNotFound means the row does not exist or does not belong to the caller.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict means the car is already booked for an overlapping window.
	ErrConflict = errors.New("store: booking conflict")
	// ErrPriceChanged means the car's current trip price is above what the
	// renter agreed to pay.
	ErrPriceChanged = errors.New("store: price changed")
	// ErrCarNotBookable means the car is missing or not listed.
	ErrCarNotBookable = errors.New("store: car not bookable")
	// ErrScheduleConflict means an owner recurrence blocks the window.
	ErrScheduleConflict = errors.New("store: owner schedule conflict")
	// ErrTooLateToCancel means the 24 hour cancellation window has passed.
	ErrTooLateToCancel = errors.New("store: too late to cancel")
)

// Reservation kinds. rental is a paid booking, rental_hold is a short
// pre-payment lock, owner is the owner blocking their own car.
const (
	KindRental     = "rental"
	KindRentalHold = "rental_hold"
	KindOwner      = "owner"
)

// User is a row of cars.users.
type User struct {
	// ID is the account uuid every other table references.
	ID string
	// Email is the address from the provider profile at last sign-in.
	Email string
	// DisplayName is the name shown to other users.
	DisplayName string
	// AvatarURL is the provider profile picture, empty when absent.
	AvatarURL string
	// CreatedAt is when the account was created.
	CreatedAt time.Time
}

// Car is a row of cars.cars. Location is split into Lat/Lng for the API.
type Car struct {
	// ID is the car uuid.
	ID string
	// OwnerID is the user who listed the car.
	OwnerID string
	// Model is make and model as the owner wrote it, like "Ford Mustang".
	Model string
	// ModelYear is the model year, nil when the owner did not give one.
	ModelYear *int
	// Lat and Lng are the pickup point in degrees.
	Lat float64
	Lng float64
	// PricePerHour is the rental price in cents.
	PricePerHour int
	// IsListed is false when the owner has hidden the car from search.
	IsListed bool
	// CreatedAt is when the car was listed.
	CreatedAt time.Time
	// UpdatedAt is the last owner edit.
	UpdatedAt time.Time
}

// Reservation is a row of cars.reservations with the range split open.
type Reservation struct {
	// ID is the reservation uuid.
	ID string
	// CarID is the booked car.
	CarID string
	// BookerID is who booked, a renter or the owner.
	BookerID string
	// Kind is rental, rental_hold, or owner.
	Kind string
	// Start and End are the booked window, half-open [Start, End).
	Start time.Time
	End   time.Time
	// Status is confirmed or cancelled. Only confirmed rows block the car.
	Status string
	// Price is the trip price in cents frozen at booking time, nil for owner
	// holds.
	Price *int
	// HoldExpiresAt is set on rental_hold rows only. A hold past it no longer
	// blocks the car.
	HoldExpiresAt *time.Time
	// CreatedAt is when the booking was made.
	CreatedAt time.Time
}

// Recurrence is a row of cars.recurrences.
type Recurrence struct {
	// ID is the schedule uuid.
	ID string
	// CarID is the car the schedule blocks.
	CarID string
	// FirstStart and FirstEnd are the first occurrence, half-open.
	FirstStart time.Time
	FirstEnd   time.Time
	// Period is weekly, monthly, or yearly.
	Period string
	// Timezone is the IANA zone occurrences keep wall-clock time in.
	Timezone string
	// Active is false once the owner cancels the schedule.
	Active bool
	// CreatedAt is when the schedule was created.
	CreatedAt time.Time
}

// AvailableCar is a search result: a car plus the price of the requested trip
// and its distance from the searcher.
type AvailableCar struct {
	Car
	// TripPrice is the whole trip in cents at the car's current rate.
	TripPrice int
	// DistanceMeters is the approximate distance from the search point.
	DistanceMeters float64
}

// Calendar is the owner's view of a car: bookings in a window plus the active
// recurring holds.
type Calendar struct {
	// Reservations are the confirmed bookings overlapping the asked window.
	Reservations []Reservation
	// Recurrences are the car's active repeating holds.
	Recurrences []Recurrence
}

// NewCar is a listing request.
type NewCar struct {
	// Lat and Lng are the pickup point in degrees.
	Lat float64
	Lng float64
	// PricePerHour is the rate in cents.
	PricePerHour int
	// Model is make and model free text, like "Ford Mustang".
	Model string
	// ModelYear is optional.
	ModelYear *int
}

// CarPatch is a partial car update. Nil fields are left unchanged.
type CarPatch struct {
	// Lat and Lng move the pickup point. They must be set together, the
	// handler validates that.
	Lat *float64
	Lng *float64
	// PricePerHour changes the rate in cents. Existing bookings keep their
	// frozen price.
	PricePerHour *int
	// IsListed hides or shows the car in search.
	IsListed *bool
}

// AvailabilityParams is a search request. RangeMeters must already be clamped
// by the caller.
type AvailabilityParams struct {
	// Lat and Lng are where the searcher is, in degrees.
	Lat float64
	Lng float64
	// RangeMeters is the search radius.
	RangeMeters float64
	// Start and End are the trip window, half-open.
	Start time.Time
	End   time.Time
	// Sort is "distance" (closest first, the handler's default) or "price"
	// (cheapest trip first). The other field breaks ties either way.
	Sort string
	// Limit and Offset page the results.
	Limit  int
	Offset int
}

// AvailabilityCountParams is a request for how many cars a search matches.
// Every field means what the same-named field on AvailabilityParams means,
// except Cap. Sort is absent because a count does not depend on the order.
type AvailabilityCountParams struct {
	Lat         float64
	Lng         float64
	RangeMeters float64
	Start       time.Time
	End         time.Time
	// Cap stops the count once this many cars match, so a count costs the
	// same in a dense city as a sparse one. A result equal to Cap means "at
	// least this many", not "exactly this many".
	Cap int
}

// OrderParams is a booking request.
type OrderParams struct {
	// CarID is the car to book.
	CarID string
	// BookerID is the signed-in user.
	BookerID string
	// Kind is rental, rental_hold, or owner.
	Kind string
	// Start and End are the trip window, half-open.
	Start time.Time
	End   time.Time
	// MaxPrice is the trip price in cents the renter saw and agreed to. The
	// booking fails with ErrPriceChanged if the current price is above it.
	// Ignored for owner holds.
	MaxPrice int
	// IdempotencyKey makes retries return the original reservation. Empty
	// disables the check.
	IdempotencyKey string
	// HoldTTL is how long a rental_hold blocks the car. Ignored otherwise.
	HoldTTL time.Duration
}

// ScheduleParams creates a recurring owner hold starting at its first
// occurrence [Start, End).
type ScheduleParams struct {
	// CarID is the car to block.
	CarID string
	// OwnerID must own the car, asserted in the insert.
	OwnerID string
	// Start and End are the first occurrence, half-open.
	Start time.Time
	End   time.Time
	// Period is weekly, monthly, or yearly.
	Period string
	// Timezone is the IANA zone occurrences keep wall-clock time in.
	Timezone string
}

// DataStore is everything the HTTP layer needs. Store implements it against
// Postgres, memstore implements it in memory for handler tests.
type DataStore interface {
	UpsertIdentity(ctx context.Context, provider, subject, email, displayName, avatarURL string) (User, error)
	CreateSession(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	UserBySessionToken(ctx context.Context, tokenHash string) (User, error)
	DeleteSession(ctx context.Context, tokenHash string) error

	CreateCar(ctx context.Context, ownerID string, params NewCar) (Car, error)
	UpdateCar(ctx context.Context, ownerID, carID string, patch CarPatch) (Car, error)
	GetCar(ctx context.Context, carID string) (Car, error)
	CarsByOwner(ctx context.Context, ownerID string) ([]Car, error)

	Availability(ctx context.Context, params AvailabilityParams) ([]AvailableCar, error)
	AvailabilityCount(ctx context.Context, params AvailabilityCountParams) (int, error)
	OrderCar(ctx context.Context, params OrderParams) (Reservation, error)
	ConfirmHold(ctx context.Context, reservationID, bookerID string) (Reservation, error)
	CancelReservation(ctx context.Context, reservationID, bookerID string) error
	MyReservations(ctx context.Context, bookerID string) ([]Reservation, error)

	CreateSchedule(ctx context.Context, params ScheduleParams) (Recurrence, error)
	DeactivateSchedule(ctx context.Context, scheduleID, ownerID string) error
	CarCalendar(ctx context.Context, carID, ownerID string, start, end time.Time) (Calendar, error)
}

// Store is the Postgres implementation of DataStore.
type Store struct {
	pool *pgxpool.Pool
}

var _ DataStore = (*Store)(nil)

// New connects a pool and pings it so a bad DATABASE_URL fails at startup,
// not on the first request.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// NewWithPool wraps an existing pool. Used by tests.
func NewWithPool(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Close releases the pool.
func (store *Store) Close() {
	store.pool.Close()
}

// Pool exposes the underlying pool for metrics collectors.
func (store *Store) Pool() *pgxpool.Pool {
	return store.pool
}

// UpsertIdentity signs a user in: it resolves (provider, subject) to an
// account, refreshing the profile, or creates the account and identity on
// first sign-in. Keyed on the provider's stable subject id, never the email,
// so an email change never forks a user, and one account can hold several
// providers.
func (store *Store) UpsertIdentity(ctx context.Context, provider, subject, email, displayName, avatarURL string) (User, error) {
	user, err := store.upsertIdentityOnce(ctx, provider, subject, email, displayName, avatarURL)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		// Two first sign-ins raced, the other one created the identity. This
		// retry takes the update path.
		user, err = store.upsertIdentityOnce(ctx, provider, subject, email, displayName, avatarURL)
	}
	if err != nil {
		return User{}, fmt.Errorf("store: upsert identity: %w", err)
	}
	return user, nil
}

func (store *Store) upsertIdentityOnce(ctx context.Context, provider, subject, email, displayName, avatarURL string) (User, error) {
	var user User
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			UPDATE cars.users u
			SET email = nullif($3, ''), display_name = $4, avatar_url = nullif($5, '')
			FROM cars.identities i
			WHERE i.provider = $1 AND i.subject = $2 AND u.id = i.user_id
			RETURNING u.id, COALESCE(u.email, ''), u.display_name, COALESCE(u.avatar_url, ''), u.created_at`,
			provider, subject, email, displayName, avatarURL)
		err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.CreatedAt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		row = tx.QueryRow(ctx, `
			INSERT INTO cars.users (email, display_name, avatar_url)
			VALUES (nullif($1, ''), $2, nullif($3, ''))
			RETURNING id, COALESCE(email, ''), display_name, COALESCE(avatar_url, ''), created_at`,
			email, displayName, avatarURL)
		if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.CreatedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO cars.identities (provider, subject, user_id) VALUES ($1, $2, $3)`,
			provider, subject, user.ID)
		return err
	})
	return user, err
}

// CreateSession stores the hash of a session token.
func (store *Store) CreateSession(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO cars.sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// UserBySessionToken resolves a live session to its user.
func (store *Store) UserBySessionToken(ctx context.Context, tokenHash string) (User, error) {
	row := store.pool.QueryRow(ctx, `
		SELECT u.id, COALESCE(u.email, ''), u.display_name, COALESCE(u.avatar_url, ''), u.created_at
		FROM cars.sessions s
		JOIN cars.users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`,
		tokenHash)
	var user User
	if err := row.Scan(&user.ID, &user.Email, &user.DisplayName, &user.AvatarURL, &user.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("store: session lookup: %w", err)
	}
	return user, nil
}

// DeleteSession logs the session out. Deleting a missing session is fine.
func (store *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := store.pool.Exec(ctx, `DELETE FROM cars.sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// CreateCar lists a car at (lat, lng) with a price in cents per hour.
func (store *Store) CreateCar(ctx context.Context, ownerID string, params NewCar) (Car, error) {
	row := store.pool.QueryRow(ctx, `
		INSERT INTO cars.cars (owner_id, location, price_per_hour, model, model_year)
		VALUES ($1, point($2, $3), $4, $5, $6)
		RETURNING `+carColumns,
		ownerID, params.Lng, params.Lat, params.PricePerHour, params.Model, params.ModelYear)
	car, err := scanCar(row)
	if err != nil {
		return Car{}, fmt.Errorf("store: create car: %w", err)
	}
	return car, nil
}

// UpdateCar applies a partial update, but only for the owner. The ownership
// check lives in the statement's WHERE so there is no read-then-write gap.
func (store *Store) UpdateCar(ctx context.Context, ownerID, carID string, patch CarPatch) (Car, error) {
	row := store.pool.QueryRow(ctx, `
		UPDATE cars.cars
		SET location = CASE WHEN $3::float8 IS NULL THEN location ELSE point($4, $3) END,
		    price_per_hour = COALESCE($5, price_per_hour),
		    is_listed = COALESCE($6, is_listed),
		    updated_at = now()
		WHERE id = $1 AND owner_id = $2
		RETURNING `+carColumns,
		carID, ownerID, patch.Lat, patch.Lng, patch.PricePerHour, patch.IsListed)
	car, err := scanCar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Car{}, ErrNotFound
		}
		return Car{}, fmt.Errorf("store: update car: %w", err)
	}
	return car, nil
}

// GetCar returns one listed car. It backs the public car endpoint, so a car
// the owner has hidden is not found: an id seen while a car was listed must
// stop resolving the moment the owner hides it. Owners read their own cars,
// hidden ones included, through CarsByOwner.
func (store *Store) GetCar(ctx context.Context, carID string) (Car, error) {
	row := store.pool.QueryRow(ctx, `
		SELECT `+carColumns+`
		FROM cars.cars WHERE id = $1 AND is_listed`, carID)
	car, err := scanCar(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Car{}, ErrNotFound
		}
		return Car{}, fmt.Errorf("store: get car: %w", err)
	}
	return car, nil
}

// CarsByOwner lists everything a host has listed, hidden cars included.
func (store *Store) CarsByOwner(ctx context.Context, ownerID string) ([]Car, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+carColumns+`
		FROM cars.cars
		WHERE owner_id = $1
		ORDER BY created_at
		LIMIT 200`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("store: cars by owner: %w", err)
	}
	defer rows.Close()
	var cars []Car
	for rows.Next() {
		car, err := scanCar(rows)
		if err != nil {
			return nil, fmt.Errorf("store: cars by owner scan: %w", err)
		}
		cars = append(cars, car)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cars by owner rows: %w", err)
	}
	return cars, nil
}

// distanceMetersSQL approximates meters from degree deltas, scaling longitude
// by cos(latitude). Good to well under 1% at city scale, which is all the
// sort needs. Must stay in step with DistanceMeters below.
const distanceMetersSQL = `111320 * sqrt(power(c.location[1] - $2, 2) + power((c.location[0] - $1) * cos(radians($2)), 2))`

// DistanceMeters approximates meters between two points the same way the
// search SQL does, so callers can recompute a result's distance for a nearby
// origin without the answers drifting apart.
func DistanceMeters(fromLat, fromLng, toLat, toLng float64) float64 {
	latDelta := toLat - fromLat
	lngDelta := (toLng - fromLng) * cosDegrees(fromLat)
	return 111320 * math.Sqrt(latDelta*latDelta+lngDelta*lngDelta)
}

// availabilityFilterSQL is the shared body of both search shapes: listed cars
// inside the padded circle, free of confirmed reservations and recurring
// holds for the whole window. The padded circle over-admits east-west by up
// to 1/cos(latitude); each shape trims to the exact meter radius itself,
// because where that trim sits decides the query plan (see the distance
// shape).
const availabilityFilterSQL = `
		SELECT c.id, c.owner_id, c.model, c.model_year, c.location[1], c.location[0], c.price_per_hour, c.is_listed, c.created_at, c.updated_at,
		       round(c.price_per_hour * $6::float8)::int AS trip_price,
		       ` + distanceMetersSQL + ` AS distance_meters
		FROM cars.cars c
		WHERE c.is_listed
		  AND c.location <@ circle(point($1, $2), $3)
		  AND NOT EXISTS (
		    SELECT 1 FROM cars.reservations r
		    WHERE r.car_id = c.id AND r.status = 'confirmed'
		      AND (r.hold_expires_at IS NULL OR r.hold_expires_at > now())
		      AND r.during && tstzrange($5, $7)
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM cars.recurrences rc
		    WHERE rc.car_id = c.id AND rc.active
		      AND cars.recurrence_overlaps(rc.first_occurrence, rc.period, rc.timezone, tstzrange($5, $7))
		  )`

// availabilityByPriceSQL must rank every car in the circle before it knows
// the cheapest, so its cost grows with circle density.
const availabilityByPriceSQL = availabilityFilterSQL + `
		  AND ` + distanceMetersSQL + ` <= $4
		ORDER BY trip_price, distance_meters, c.id
		LIMIT $8 OFFSET $9`

// availabilityByDistanceSQL streams candidates nearest-first out of the GiST
// index and stops once the page is full, so dense cities cost the same as
// sparse ones. Two traps shape it. The <-> operator orders by degrees, which
// understates east-west meters by cos(latitude), so it over-fetches 2x
// (enough through latitude 60) and the outer query ranks by exact meters.
// And the meter radius trim must stay OUT of the subquery: as an opaque
// expression the planner gives it fixed selectivity, the row estimate drops
// under the LIMIT, and it swaps the ordered index scan for a
// scan-everything-and-sort plan that is ~25x slower in a dense city. Trimming
// outside can only shorten a page's tail, never lose a car: an in-radius row
// keeps its position in the degree-ordered stream whichever page it falls on.
const availabilityByDistanceSQL = `
		SELECT * FROM (` + availabilityFilterSQL + `
		ORDER BY c.location <-> point($1, $2)
		LIMIT 2 * ($8::int + $9::int)
		) sub
		WHERE distance_meters <= $4
		ORDER BY distance_meters, trip_price, id
		LIMIT $8 OFFSET $9`

// availabilityCountSQL counts matches without ordering them, stopping at the
// caller's cap. It reuses the distance variant's degree-ordered index scan and
// its 2x over-fetch, so the count sees the same candidate pool the deepest
// reachable page does and the two always agree. Counting the price variant the
// same way is not just allowed but cheaper than paging it: with no ORDER BY,
// Postgres stops at the cap instead of ranking the whole circle.
const availabilityCountSQL = `
		SELECT count(*) FROM (
		  SELECT 1 FROM (` + availabilityFilterSQL + `
		    ORDER BY c.location <-> point($1, $2)
		    LIMIT 2 * $8::int
		  ) sub
		  WHERE distance_meters <= $4
		  LIMIT $8::int
		) counted`

// AvailabilityCount reports how many cars match a search, counting no further
// than params.Cap. It exists so search can show how many results there are and
// how many pages that is, which a page of rows cannot say on its own.
//
// The cap is what keeps this affordable: without it a count would have to walk
// every car in the circle, which is exactly the density-proportional cost the
// paged query is written to avoid.
func (store *Store) AvailabilityCount(ctx context.Context, params AvailabilityCountParams) (int, error) {
	paddedRadiusDegrees := params.RangeMeters / (111320 * cosDegrees(params.Lat))
	tripHours := params.End.Sub(params.Start).Hours()
	var total int
	// $6 (trip hours) and $8 (the cap) are the only params the projection
	// needs, but the filter is shared verbatim so every placeholder is bound.
	err := store.pool.QueryRow(ctx, availabilityCountSQL,
		params.Lng, params.Lat, paddedRadiusDegrees, params.RangeMeters,
		params.Start, tripHours, params.End, params.Cap).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: availability count: %w", err)
	}
	return total, nil
}

// Availability lists cars free for the whole window. Sort "distance" (the
// handler's default) walks the location index nearest-first and early-exits;
// sort "price" ranks the whole circle cheapest-first. The GiST circle filter
// is padded (longitude degrees shrink with latitude) and the exact meter
// distance re-filters below it.
func (store *Store) Availability(ctx context.Context, params AvailabilityParams) ([]AvailableCar, error) {
	paddedRadiusDegrees := params.RangeMeters / (111320 * cosDegrees(params.Lat))
	tripHours := params.End.Sub(params.Start).Hours()
	query := availabilityByPriceSQL
	if params.Sort == "distance" {
		query = availabilityByDistanceSQL
	}
	rows, err := store.pool.Query(ctx, query,
		params.Lng, params.Lat, paddedRadiusDegrees, params.RangeMeters,
		params.Start, tripHours, params.End, params.Limit, params.Offset)
	if err != nil {
		return nil, fmt.Errorf("store: availability: %w", err)
	}
	defer rows.Close()

	var results []AvailableCar
	for rows.Next() {
		var result AvailableCar
		if err := rows.Scan(&result.ID, &result.OwnerID, &result.Model, &result.ModelYear, &result.Lat, &result.Lng,
			&result.PricePerHour, &result.IsListed, &result.CreatedAt, &result.UpdatedAt,
			&result.TripPrice, &result.DistanceMeters); err != nil {
			return nil, fmt.Errorf("store: availability scan: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: availability rows: %w", err)
	}
	return results, nil
}

// OrderCar books a car. One transaction: reap expired holds on this car, then
// a single conditional INSERT..SELECT that freezes the price, checks the
// listing, the renter's agreed price, and the owner's recurring schedule. The
// exclusion constraint rejects overlaps, so two racing renters serialize in
// the database and exactly one wins.
//
// Reservations always beat recurrences: this insert only checks recurrences
// visible at its snapshot. If an owner schedules concurrently, the renter's
// booking stands and the owner misses that occurrence.
func (store *Store) OrderCar(ctx context.Context, params OrderParams) (Reservation, error) {
	var reservation Reservation
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		// A retry of a request that already booked overlaps its own row, so
		// the exclusion constraint would fire before the idempotency index
		// gets a say. Check the key first.
		if params.IdempotencyKey != "" {
			existing, err := reservationByIdempotencyKey(ctx, tx, params.BookerID, params.IdempotencyKey)
			if err == nil {
				reservation = existing
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}

		if params.Kind == KindOwner {
			var ownerID string
			err := tx.QueryRow(ctx, `SELECT owner_id FROM cars.cars WHERE id = $1`, params.CarID).Scan(&ownerID)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && ownerID != params.BookerID) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("owner check: %w", err)
			}
		}

		// Serialize bookings per car. Exclusion constraints check conflicts
		// by inserting then scanning, so two concurrent same-window inserts
		// wait on each other and Postgres has to break the tie with a
		// deadlock error (benchmarked: 4 qps and 40P01s under a pile-up on
		// one car). With the per-car lock, writers on one car queue in order,
		// the precheck below answers losers from the index in microseconds,
		// and bookings on other cars are untouched.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, params.CarID); err != nil {
			return fmt.Errorf("car lock: %w", err)
		}

		reaped, err := tx.Exec(ctx, `
			DELETE FROM cars.reservations
			WHERE car_id = $1 AND kind = 'rental_hold' AND status = 'confirmed' AND hold_expires_at <= now()`,
			params.CarID)
		if err != nil {
			return fmt.Errorf("reap expired holds: %w", err)
		}
		metrics.HoldsExpiredTotal.Add(float64(reaped.RowsAffected()))

		// Contention shedding, not correctness: answers doomed attempts from
		// the index instead of letting them attempt the insert. The exclusion
		// constraint below remains the only authority.
		var blocked bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM cars.reservations
				WHERE car_id = $1 AND status = 'confirmed'
				  AND (hold_expires_at IS NULL OR hold_expires_at > now())
				  AND during && tstzrange($2, $3)
			)`, params.CarID, params.Start, params.End).Scan(&blocked); err != nil {
			return fmt.Errorf("conflict precheck: %w", err)
		}
		if blocked {
			return ErrConflict
		}

		tripHours := params.End.Sub(params.Start).Hours()
		row := tx.QueryRow(ctx, `
			INSERT INTO cars.reservations (car_id, booker_id, kind, during, price, hold_expires_at, idempotency_key)
			SELECT c.id, $2, $3, tstzrange($4, $5),
			       CASE WHEN $3 = 'owner' THEN NULL ELSE round(c.price_per_hour * $6::float8)::int END,
			       CASE WHEN $3 = 'rental_hold' THEN now() + make_interval(secs => $7) ELSE NULL END,
			       nullif($8, '')
			FROM cars.cars c
			WHERE c.id = $1
			  AND (($3 = 'owner' AND c.owner_id = $2) OR ($3 <> 'owner' AND c.is_listed))
			  AND ($3 = 'owner' OR round(c.price_per_hour * $6::float8)::int <= $9)
			  AND ($3 = 'owner' OR NOT EXISTS (
			    SELECT 1 FROM cars.recurrences r
			    WHERE r.car_id = c.id AND r.active
			      AND cars.recurrence_overlaps(r.first_occurrence, r.period, r.timezone, tstzrange($4, $5))
			  ))
			RETURNING id, car_id, booker_id, kind, lower(during), upper(during), status, price, hold_expires_at, created_at`,
			params.CarID, params.BookerID, params.Kind, params.Start, params.End,
			tripHours, params.HoldTTL.Seconds(), params.IdempotencyKey, params.MaxPrice)
		reservation, err = scanReservation(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.classifyRejectedOrder(ctx, tx, params)
		}
		return err
	})
	if err == nil {
		return reservation, nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23P01" || pgErr.Code == "23505") {
		// A concurrent identical request may have won the race. If our key
		// already has a reservation, this request succeeded once already.
		if params.IdempotencyKey != "" {
			existing, replayErr := reservationByIdempotencyKey(ctx, store.pool, params.BookerID, params.IdempotencyKey)
			if replayErr == nil {
				return existing, nil
			}
		}
		if pgErr.Code == "23P01" {
			return Reservation{}, ErrConflict
		}
	}
	if isStoreSentinel(err) {
		return Reservation{}, err
	}
	return Reservation{}, fmt.Errorf("store: order car: %w", err)
}

// classifyRejectedOrder figures out why the conditional insert matched no row,
// inside the same transaction so it reads the same snapshot.
func (store *Store) classifyRejectedOrder(ctx context.Context, tx pgx.Tx, params OrderParams) error {
	var ownerID string
	var isListed bool
	var pricePerHour int
	err := tx.QueryRow(ctx,
		`SELECT owner_id, is_listed, price_per_hour FROM cars.cars WHERE id = $1`,
		params.CarID).Scan(&ownerID, &isListed, &pricePerHour)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCarNotBookable
	}
	if err != nil {
		return fmt.Errorf("classify rejected order: %w", err)
	}
	if params.Kind == KindOwner {
		if ownerID != params.BookerID {
			return ErrNotFound
		}
		return ErrCarNotBookable
	}
	if !isListed {
		return ErrCarNotBookable
	}
	tripPrice := int(float64(pricePerHour)*params.End.Sub(params.Start).Hours() + 0.5)
	if tripPrice > params.MaxPrice {
		return ErrPriceChanged
	}
	return ErrScheduleConflict
}

// queryRower is the slice of pgx.Tx and pgxpool.Pool this file needs.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// reservationByIdempotencyKey returns the reservation an earlier identical
// request already created, so retries are exactly-once.
func reservationByIdempotencyKey(ctx context.Context, database queryRower, bookerID, key string) (Reservation, error) {
	row := database.QueryRow(ctx, `
		SELECT id, car_id, booker_id, kind, lower(during), upper(during), status, price, hold_expires_at, created_at
		FROM cars.reservations
		WHERE booker_id = $1 AND idempotency_key = $2`,
		bookerID, key)
	reservation, err := scanReservation(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Reservation{}, ErrNotFound
		}
		return Reservation{}, fmt.Errorf("store: idempotent replay: %w", err)
	}
	return reservation, nil
}

// ConfirmHold turns a live hold into a rental. Single conditional UPDATE, so
// an expired or foreign hold can never be confirmed, no matter the timing.
func (store *Store) ConfirmHold(ctx context.Context, reservationID, bookerID string) (Reservation, error) {
	row := store.pool.QueryRow(ctx, `
		UPDATE cars.reservations
		SET kind = 'rental', hold_expires_at = NULL
		WHERE id = $1 AND booker_id = $2 AND kind = 'rental_hold' AND status = 'confirmed' AND hold_expires_at > now()
		RETURNING id, car_id, booker_id, kind, lower(during), upper(during), status, price, hold_expires_at, created_at`,
		reservationID, bookerID)
	reservation, err := scanReservation(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Reservation{}, ErrNotFound
		}
		return Reservation{}, fmt.Errorf("store: confirm hold: %w", err)
	}
	return reservation, nil
}

// CancelReservation cancels the caller's booking. The policy matches the big
// marketplaces (Turo and Getaround both draw the same line): free up to 24
// hours before the start, plus a one hour grace period after booking so a
// mistake made inside that window is not a trap. Live holds cancel any time.
// The rule sits in the UPDATE's WHERE, then a read disambiguates the error.
func (store *Store) CancelReservation(ctx context.Context, reservationID, bookerID string) error {
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE cars.reservations
			SET status = 'cancelled'
			WHERE id = $1 AND booker_id = $2 AND status = 'confirmed'
			  AND (kind = 'rental_hold'
			       OR lower(during) > now() + interval '24 hours'
			       OR (created_at > now() - interval '1 hour' AND lower(during) > now()))`,
			reservationID, bookerID)
		if err != nil {
			return fmt.Errorf("cancel: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM cars.reservations WHERE id = $1 AND booker_id = $2 AND status = 'confirmed')`,
			reservationID, bookerID).Scan(&exists); err != nil {
			return fmt.Errorf("cancel lookup: %w", err)
		}
		if exists {
			return ErrTooLateToCancel
		}
		return ErrNotFound
	})
	if err != nil && !isStoreSentinel(err) {
		return fmt.Errorf("store: cancel reservation: %w", err)
	}
	return err
}

// MyReservations lists the caller's bookings, upcoming first.
func (store *Store) MyReservations(ctx context.Context, bookerID string) ([]Reservation, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id, car_id, booker_id, kind, lower(during), upper(during), status, price, hold_expires_at, created_at
		FROM cars.reservations
		WHERE booker_id = $1
		ORDER BY lower(during) DESC
		LIMIT 100`, bookerID)
	if err != nil {
		return nil, fmt.Errorf("store: my reservations: %w", err)
	}
	defer rows.Close()
	return collectReservations(rows)
}

// CreateSchedule adds a recurring owner hold. Ownership is asserted in the
// INSERT's WHERE. It never checks for booking conflicts on purpose: existing
// renter reservations win, the owner just misses those occurrences.
func (store *Store) CreateSchedule(ctx context.Context, params ScheduleParams) (Recurrence, error) {
	row := store.pool.QueryRow(ctx, `
		INSERT INTO cars.recurrences (car_id, first_occurrence, period, timezone)
		SELECT c.id, tstzrange($3, $4), $5, $6
		FROM cars.cars c
		WHERE c.id = $1 AND c.owner_id = $2
		RETURNING id, car_id, lower(first_occurrence), upper(first_occurrence), period, timezone, active, created_at`,
		params.CarID, params.OwnerID, params.Start, params.End, params.Period, params.Timezone)
	var recurrence Recurrence
	err := row.Scan(&recurrence.ID, &recurrence.CarID, &recurrence.FirstStart, &recurrence.FirstEnd,
		&recurrence.Period, &recurrence.Timezone, &recurrence.Active, &recurrence.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Recurrence{}, ErrNotFound
		}
		return Recurrence{}, fmt.Errorf("store: create schedule: %w", err)
	}
	return recurrence, nil
}

// DeactivateSchedule cancels a recurring hold, owner only.
func (store *Store) DeactivateSchedule(ctx context.Context, scheduleID, ownerID string) error {
	tag, err := store.pool.Exec(ctx, `
		UPDATE cars.recurrences rc
		SET active = false
		FROM cars.cars c
		WHERE rc.id = $1 AND c.id = rc.car_id AND c.owner_id = $2 AND rc.active`,
		scheduleID, ownerID)
	if err != nil {
		return fmt.Errorf("store: deactivate schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CarCalendar returns bookings overlapping the window plus active recurrences,
// for the car's owner only.
func (store *Store) CarCalendar(ctx context.Context, carID, ownerID string, start, end time.Time) (Calendar, error) {
	var calendar Calendar
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT owner_id FROM cars.cars WHERE id = $1`, carID).Scan(&owner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("calendar owner lookup: %w", err)
		}
		if owner != ownerID {
			return ErrNotFound
		}

		rows, err := tx.Query(ctx, `
			SELECT id, car_id, booker_id, kind, lower(during), upper(during), status, price, hold_expires_at, created_at
			FROM cars.reservations
			WHERE car_id = $1 AND status = 'confirmed' AND during && tstzrange($2, $3)
			ORDER BY lower(during)`, carID, start, end)
		if err != nil {
			return fmt.Errorf("calendar reservations: %w", err)
		}
		calendar.Reservations, err = collectReservations(rows)
		if err != nil {
			return err
		}

		recurrenceRows, err := tx.Query(ctx, `
			SELECT id, car_id, lower(first_occurrence), upper(first_occurrence), period, timezone, active, created_at
			FROM cars.recurrences
			WHERE car_id = $1 AND active
			ORDER BY created_at`, carID)
		if err != nil {
			return fmt.Errorf("calendar recurrences: %w", err)
		}
		defer recurrenceRows.Close()
		for recurrenceRows.Next() {
			var recurrence Recurrence
			if err := recurrenceRows.Scan(&recurrence.ID, &recurrence.CarID, &recurrence.FirstStart, &recurrence.FirstEnd,
				&recurrence.Period, &recurrence.Timezone, &recurrence.Active, &recurrence.CreatedAt); err != nil {
				return fmt.Errorf("calendar recurrence scan: %w", err)
			}
			calendar.Recurrences = append(calendar.Recurrences, recurrence)
		}
		return recurrenceRows.Err()
	})
	if err != nil {
		if isStoreSentinel(err) {
			return Calendar{}, err
		}
		return Calendar{}, fmt.Errorf("store: car calendar: %w", err)
	}
	return calendar, nil
}

// CountDoubleBookedPairs is the invariant the exclusion constraint makes
// impossible. The constraint alone is what guarantees correctness. This check
// exists to catch operational accidents the constraint cannot survive, like a
// migration dropping it or a restore from a corrupted dump, and to feed the
// critical alert that proves the guarantee is still standing. It only scans
// reservations that touch the present or future: past rows are immutable
// history, rescanning them costs time and can never change the answer.
func (store *Store) CountDoubleBookedPairs(ctx context.Context) (int, error) {
	var count int
	err := store.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM cars.reservations a
		JOIN cars.reservations b ON a.car_id = b.car_id AND a.id < b.id AND a.during && b.during
		WHERE a.status = 'confirmed' AND b.status = 'confirmed'
		  AND a.during && tstzrange(now(), NULL)`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: double booking invariant: %w", err)
	}
	return count, nil
}

// carColumns is every cars.cars column scanCar expects, in scan order.
const carColumns = `id, owner_id, model, model_year, location[1], location[0], price_per_hour, is_listed, created_at, updated_at`

func scanCar(row pgx.Row) (Car, error) {
	var car Car
	err := row.Scan(&car.ID, &car.OwnerID, &car.Model, &car.ModelYear, &car.Lat, &car.Lng,
		&car.PricePerHour, &car.IsListed, &car.CreatedAt, &car.UpdatedAt)
	return car, err
}

func scanReservation(row pgx.Row) (Reservation, error) {
	var reservation Reservation
	err := row.Scan(&reservation.ID, &reservation.CarID, &reservation.BookerID, &reservation.Kind,
		&reservation.Start, &reservation.End, &reservation.Status, &reservation.Price,
		&reservation.HoldExpiresAt, &reservation.CreatedAt)
	return reservation, err
}

func collectReservations(rows pgx.Rows) ([]Reservation, error) {
	defer rows.Close()
	var reservations []Reservation
	for rows.Next() {
		reservation, err := scanReservation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reservation scan: %w", err)
		}
		reservations = append(reservations, reservation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reservation rows: %w", err)
	}
	return reservations, nil
}

func isStoreSentinel(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrPriceChanged) ||
		errors.Is(err, ErrCarNotBookable) || errors.Is(err, ErrScheduleConflict) || errors.Is(err, ErrTooLateToCancel)
}

func cosDegrees(degrees float64) float64 {
	return math.Cos(degrees * math.Pi / 180)
}
