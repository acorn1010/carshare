// Package memstore is an in-memory DataStore so handler tests run with zero
// infrastructure. It mirrors the Postgres semantics closely enough for flow
// tests, but the real concurrency guarantees live in the database and are
// proven by the store integration tests, not here.
package memstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"carshare/internal/store"
)

// Store implements store.DataStore in memory.
type Store struct {
	mutex  sync.Mutex
	nextID int
	users  map[string]store.User
	// identities maps provider+"\x00"+subject to a user id.
	identities   map[string]string
	sessions     map[string]session
	cars         map[string]store.Car
	reservations map[string]store.Reservation
	recurrences  map[string]store.Recurrence
	// idempotency maps bookerID+"\x00"+key to a reservation id.
	idempotency map[string]string
}

type session struct {
	userID    string
	expiresAt time.Time
}

var _ store.DataStore = (*Store)(nil)

// New returns an empty memstore.
func New() *Store {
	return &Store{
		users:        map[string]store.User{},
		identities:   map[string]string{},
		sessions:     map[string]session{},
		cars:         map[string]store.Car{},
		reservations: map[string]store.Reservation{},
		recurrences:  map[string]store.Recurrence{},
		idempotency:  map[string]string{},
	}
}

func (memory *Store) newID(prefix string) string {
	memory.nextID++
	return fmt.Sprintf("%s-%d", prefix, memory.nextID)
}

func (memory *Store) UpsertIdentity(_ context.Context, provider, subject, email, displayName, avatarURL string) (store.User, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	identityKey := provider + "\x00" + subject
	if id, ok := memory.identities[identityKey]; ok {
		user := memory.users[id]
		user.Email, user.DisplayName, user.AvatarURL = email, displayName, avatarURL
		memory.users[id] = user
		return user, nil
	}
	user := store.User{ID: memory.newID("user"), Email: email,
		DisplayName: displayName, AvatarURL: avatarURL, CreatedAt: time.Now()}
	memory.users[user.ID] = user
	memory.identities[identityKey] = user.ID
	return user, nil
}

func (memory *Store) CreateSession(_ context.Context, userID, tokenHash string, expiresAt time.Time) error {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	memory.sessions[tokenHash] = session{userID: userID, expiresAt: expiresAt}
	return nil
}

func (memory *Store) UserBySessionToken(_ context.Context, tokenHash string) (store.User, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	entry, ok := memory.sessions[tokenHash]
	if !ok || entry.expiresAt.Before(time.Now()) {
		return store.User{}, store.ErrNotFound
	}
	return memory.users[entry.userID], nil
}

func (memory *Store) DeleteSession(_ context.Context, tokenHash string) error {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	delete(memory.sessions, tokenHash)
	return nil
}

func (memory *Store) CreateCar(_ context.Context, ownerID string, lat, lng float64, pricePerHour int) (store.Car, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	car := store.Car{ID: memory.newID("car"), OwnerID: ownerID, Lat: lat, Lng: lng,
		PricePerHour: pricePerHour, IsListed: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	memory.cars[car.ID] = car
	return car, nil
}

func (memory *Store) UpdateCar(_ context.Context, ownerID, carID string, patch store.CarPatch) (store.Car, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	car, ok := memory.cars[carID]
	if !ok || car.OwnerID != ownerID {
		return store.Car{}, store.ErrNotFound
	}
	if patch.Lat != nil {
		car.Lat, car.Lng = *patch.Lat, *patch.Lng
	}
	if patch.PricePerHour != nil {
		car.PricePerHour = *patch.PricePerHour
	}
	if patch.IsListed != nil {
		car.IsListed = *patch.IsListed
	}
	car.UpdatedAt = time.Now()
	memory.cars[carID] = car
	return car, nil
}

func (memory *Store) GetCar(_ context.Context, carID string) (store.Car, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	car, ok := memory.cars[carID]
	if !ok {
		return store.Car{}, store.ErrNotFound
	}
	return car, nil
}

func (memory *Store) Availability(_ context.Context, params store.AvailabilityParams) ([]store.AvailableCar, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	var results []store.AvailableCar
	tripHours := params.End.Sub(params.Start).Hours()
	for _, car := range memory.cars {
		if !car.IsListed {
			continue
		}
		distance := distanceMeters(params.Lat, params.Lng, car.Lat, car.Lng)
		if distance > params.RangeMeters {
			continue
		}
		if memory.carBlocked(car.ID, params.Start, params.End) {
			continue
		}
		results = append(results, store.AvailableCar{
			Car:            car,
			TripPrice:      int(math.Round(float64(car.PricePerHour) * tripHours)),
			DistanceMeters: distance,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].TripPrice != results[j].TripPrice {
			return results[i].TripPrice < results[j].TripPrice
		}
		if results[i].DistanceMeters != results[j].DistanceMeters {
			return results[i].DistanceMeters < results[j].DistanceMeters
		}
		return results[i].ID < results[j].ID
	})
	if params.Offset >= len(results) {
		return nil, nil
	}
	results = results[params.Offset:]
	if len(results) > params.Limit {
		results = results[:params.Limit]
	}
	return results, nil
}

func (memory *Store) OrderCar(_ context.Context, params store.OrderParams) (store.Reservation, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()

	idempotencyKey := params.BookerID + "\x00" + params.IdempotencyKey
	if params.IdempotencyKey != "" {
		if id, ok := memory.idempotency[idempotencyKey]; ok {
			return memory.reservations[id], nil
		}
	}

	car, ok := memory.cars[params.CarID]
	if !ok {
		return store.Reservation{}, store.ErrCarNotBookable
	}
	if params.Kind == store.KindOwner {
		if car.OwnerID != params.BookerID {
			return store.Reservation{}, store.ErrNotFound
		}
	} else {
		if !car.IsListed {
			return store.Reservation{}, store.ErrCarNotBookable
		}
		tripPrice := int(math.Round(float64(car.PricePerHour) * params.End.Sub(params.Start).Hours()))
		if tripPrice > params.MaxPrice {
			return store.Reservation{}, store.ErrPriceChanged
		}
		for _, recurrence := range memory.recurrences {
			if recurrence.CarID == car.ID && recurrence.Active &&
				recurrenceOverlaps(recurrence, params.Start, params.End) {
				return store.Reservation{}, store.ErrScheduleConflict
			}
		}
	}
	if memory.reservationOverlap(car.ID, params.Start, params.End) {
		return store.Reservation{}, store.ErrConflict
	}

	reservation := store.Reservation{
		ID: memory.newID("resv"), CarID: car.ID, BookerID: params.BookerID, Kind: params.Kind,
		Start: params.Start, End: params.End, Status: "confirmed", CreatedAt: time.Now(),
	}
	if params.Kind != store.KindOwner {
		price := int(math.Round(float64(car.PricePerHour) * params.End.Sub(params.Start).Hours()))
		reservation.Price = &price
	}
	if params.Kind == store.KindRentalHold {
		expiry := time.Now().Add(params.HoldTTL)
		reservation.HoldExpiresAt = &expiry
	}
	memory.reservations[reservation.ID] = reservation
	if params.IdempotencyKey != "" {
		memory.idempotency[idempotencyKey] = reservation.ID
	}
	return reservation, nil
}

func (memory *Store) ConfirmHold(_ context.Context, reservationID, bookerID string) (store.Reservation, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	reservation, ok := memory.reservations[reservationID]
	if !ok || reservation.BookerID != bookerID || reservation.Kind != store.KindRentalHold ||
		reservation.Status != "confirmed" || reservation.HoldExpiresAt == nil || reservation.HoldExpiresAt.Before(time.Now()) {
		return store.Reservation{}, store.ErrNotFound
	}
	reservation.Kind = store.KindRental
	reservation.HoldExpiresAt = nil
	memory.reservations[reservationID] = reservation
	return reservation, nil
}

func (memory *Store) CancelReservation(_ context.Context, reservationID, bookerID string) error {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	reservation, ok := memory.reservations[reservationID]
	if !ok || reservation.BookerID != bookerID || reservation.Status != "confirmed" {
		return store.ErrNotFound
	}
	withinBookingGrace := reservation.CreatedAt.After(time.Now().Add(-time.Hour)) && reservation.Start.After(time.Now())
	if reservation.Kind != store.KindRentalHold && !reservation.Start.After(time.Now().Add(24*time.Hour)) && !withinBookingGrace {
		return store.ErrTooLateToCancel
	}
	reservation.Status = "cancelled"
	memory.reservations[reservationID] = reservation
	return nil
}

func (memory *Store) MyReservations(_ context.Context, bookerID string) ([]store.Reservation, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	var reservations []store.Reservation
	for _, reservation := range memory.reservations {
		if reservation.BookerID == bookerID {
			reservations = append(reservations, reservation)
		}
	}
	sort.Slice(reservations, func(i, j int) bool { return reservations[i].Start.After(reservations[j].Start) })
	return reservations, nil
}

func (memory *Store) CreateSchedule(_ context.Context, params store.ScheduleParams) (store.Recurrence, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	car, ok := memory.cars[params.CarID]
	if !ok || car.OwnerID != params.OwnerID {
		return store.Recurrence{}, store.ErrNotFound
	}
	recurrence := store.Recurrence{ID: memory.newID("sched"), CarID: params.CarID,
		FirstStart: params.Start, FirstEnd: params.End, Period: params.Period,
		Timezone: params.Timezone, Active: true, CreatedAt: time.Now()}
	memory.recurrences[recurrence.ID] = recurrence
	return recurrence, nil
}

func (memory *Store) DeactivateSchedule(_ context.Context, scheduleID, ownerID string) error {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	recurrence, ok := memory.recurrences[scheduleID]
	if !ok || !recurrence.Active {
		return store.ErrNotFound
	}
	car := memory.cars[recurrence.CarID]
	if car.OwnerID != ownerID {
		return store.ErrNotFound
	}
	recurrence.Active = false
	memory.recurrences[scheduleID] = recurrence
	return nil
}

func (memory *Store) CarCalendar(_ context.Context, carID, ownerID string, start, end time.Time) (store.Calendar, error) {
	memory.mutex.Lock()
	defer memory.mutex.Unlock()
	car, ok := memory.cars[carID]
	if !ok || car.OwnerID != ownerID {
		return store.Calendar{}, store.ErrNotFound
	}
	var calendar store.Calendar
	for _, reservation := range memory.reservations {
		if reservation.CarID == carID && reservation.Status == "confirmed" &&
			reservation.Start.Before(end) && reservation.End.After(start) {
			calendar.Reservations = append(calendar.Reservations, reservation)
		}
	}
	sort.Slice(calendar.Reservations, func(i, j int) bool {
		return calendar.Reservations[i].Start.Before(calendar.Reservations[j].Start)
	})
	for _, recurrence := range memory.recurrences {
		if recurrence.CarID == carID && recurrence.Active {
			calendar.Recurrences = append(calendar.Recurrences, recurrence)
		}
	}
	return calendar, nil
}

// carBlocked reports whether any live reservation or recurrence covers part of
// the window.
func (memory *Store) carBlocked(carID string, start, end time.Time) bool {
	if memory.reservationOverlap(carID, start, end) {
		return true
	}
	for _, recurrence := range memory.recurrences {
		if recurrence.CarID == carID && recurrence.Active && recurrenceOverlaps(recurrence, start, end) {
			return true
		}
	}
	return false
}

func (memory *Store) reservationOverlap(carID string, start, end time.Time) bool {
	now := time.Now()
	for _, reservation := range memory.reservations {
		if reservation.CarID != carID || reservation.Status != "confirmed" {
			continue
		}
		if reservation.HoldExpiresAt != nil && reservation.HoldExpiresAt.Before(now) {
			continue
		}
		if reservation.Start.Before(end) && reservation.End.After(start) {
			return true
		}
	}
	return false
}

// recurrenceOverlaps walks occurrences in the recurrence's timezone. Note: Go
// normalizes Jan 31 + 1 month to Mar 2 where Postgres clamps to Feb 28. Flow
// tests avoid that edge, the SQL tests pin the real behavior.
func recurrenceOverlaps(recurrence store.Recurrence, start, end time.Time) bool {
	location, err := time.LoadLocation(recurrence.Timezone)
	if err != nil {
		location = time.UTC
	}
	duration := recurrence.FirstEnd.Sub(recurrence.FirstStart)
	occurrence := recurrence.FirstStart.In(location)
	for i := 0; i < 100_000; i++ {
		if !occurrence.Before(end) {
			return false
		}
		if occurrence.Add(duration).After(start) {
			return true
		}
		switch recurrence.Period {
		case "weekly":
			occurrence = occurrence.AddDate(0, 0, 7)
		case "monthly":
			occurrence = occurrence.AddDate(0, 1, 0)
		default:
			occurrence = occurrence.AddDate(1, 0, 0)
		}
	}
	return false
}

func distanceMeters(lat1, lng1, lat2, lng2 float64) float64 {
	latDelta := lat2 - lat1
	lngDelta := (lng2 - lng1) * math.Cos(lat1*math.Pi/180)
	return 111320 * math.Sqrt(latDelta*latDelta+lngDelta*lngDelta)
}
