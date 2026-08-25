package httpapi

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"carshare/internal/metrics"
	"carshare/internal/store"
)

// Booking windows must be at least minTripDuration and at most
// maxTripDuration. 90 days sits between the two big marketplaces, Getaround
// caps trips at 30 days while Turo sells month-plus rentals, and it bounds how
// far one booking can lock a calendar without turning rentals into leases.
const (
	minTripDuration = 15 * time.Minute
	maxTripDuration = 90 * 24 * time.Hour
	// pastGrace forgives clock skew when a booking starts "now".
	pastGrace = time.Minute
	// searchFromHorizon is how far ahead availability will search. Bookings
	// have no such product limit, but search windows feed cache keys, so
	// unbounded dates would let anyone mint unlimited keys.
	searchFromHorizon = 365 * 24 * time.Hour
)

// carResponse is the public JSON shape of a car.
type carResponse struct {
	// ID is the car uuid.
	ID string `json:"id"`
	// OwnerID is the listing user's uuid.
	OwnerID string `json:"owner_id"`
	// Model is make and model free text, like "Ford Mustang".
	Model string `json:"model"`
	// ModelYear is absent when the owner did not give one.
	ModelYear *int `json:"model_year,omitempty"`
	// Lat and Lng are the pickup point in degrees.
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
	// PricePerHour is the current rate in cents.
	PricePerHour int `json:"price_per_hour"`
	// IsListed is false when the owner has hidden the car from search.
	IsListed bool `json:"is_listed"`
}

func toCarResponse(car store.Car) carResponse {
	return carResponse{ID: car.ID, OwnerID: car.OwnerID, Model: car.Model, ModelYear: car.ModelYear,
		Lat: car.Lat, Lng: car.Lng, PricePerHour: car.PricePerHour, IsListed: car.IsListed}
}

// reservationResponse is the public JSON shape of a booking.
type reservationResponse struct {
	// ID is the reservation uuid, used to confirm or cancel.
	ID string `json:"id"`
	// CarID is the booked car.
	CarID string `json:"car_id"`
	// Kind is rental, rental_hold, or owner.
	Kind string `json:"kind"`
	// Status is confirmed or cancelled.
	Status string `json:"status"`
	// Start and End are the booked window, half-open.
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Price is the frozen trip price in cents, absent on owner holds.
	Price *int `json:"price,omitempty"`
	// HoldExpiresAt is when a rental_hold stops blocking the car.
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
}

func toReservationResponse(reservation store.Reservation) reservationResponse {
	return reservationResponse{ID: reservation.ID, CarID: reservation.CarID, Kind: reservation.Kind,
		Status: reservation.Status, Start: reservation.Start, End: reservation.End,
		Price: reservation.Price, HoldExpiresAt: reservation.HoldExpiresAt}
}

// handleCreateCar lists a new car owned by the caller.
func (server *Server) handleCreateCar(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Lat          float64 `json:"lat"`
		Lng          float64 `json:"lng"`
		PricePerHour int     `json:"price_per_hour"`
		Model        string  `json:"model"`
		ModelYear    *int    `json:"model_year"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	currentYear := time.Now().Year()
	switch {
	case !validCoordinates(body.Lat, body.Lng) || body.PricePerHour < 0:
		writeError(writer, http.StatusBadRequest, "bad_request", "lat/lng out of range or negative price")
		return
	case len(body.Model) > 80:
		writeError(writer, http.StatusBadRequest, "bad_request", "model is too long")
		return
	case body.ModelYear != nil && (*body.ModelYear < 1900 || *body.ModelYear > currentYear+1):
		writeError(writer, http.StatusBadRequest, "bad_request", "model_year looks wrong")
		return
	}
	car, err := server.params.Store.CreateCar(request.Context(), currentUser(request).ID, store.NewCar{
		Lat: body.Lat, Lng: body.Lng, PricePerHour: body.PricePerHour,
		Model: body.Model, ModelYear: body.ModelYear,
	})
	if err != nil {
		slog.Error("create car", slog.String("error", err.Error()))
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, toCarResponse(car))
}

// handleGetCar returns one listed car, which is how a client quotes the
// current price before booking. The route is public, so the response is the
// slim shape search uses: no owner_id (who owns a car is nobody's business),
// no is_listed (always true here, the store hides hidden cars), coordinates
// at 6 decimals.
func (server *Server) handleGetCar(writer http.ResponseWriter, request *http.Request) {
	car, err := server.params.Store.GetCar(request.Context(), request.PathValue("id"))
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	type listedCarResponse struct {
		ID           string  `json:"id"`
		Model        string  `json:"model"`
		ModelYear    *int    `json:"model_year,omitempty"`
		Lat          float64 `json:"lat"`
		Lng          float64 `json:"lng"`
		PricePerHour int     `json:"price_per_hour"`
	}
	writeJSON(writer, http.StatusOK, listedCarResponse{
		ID: car.ID, Model: car.Model, ModelYear: car.ModelYear,
		Lat: math.Round(car.Lat*1e6) / 1e6, Lng: math.Round(car.Lng*1e6) / 1e6,
		PricePerHour: car.PricePerHour,
	})
}

// handleUpdateCar applies an owner's partial edit: price, location, or
// listing visibility. Ownership is enforced in the store's UPDATE.
func (server *Server) handleUpdateCar(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Lat          *float64 `json:"lat"`
		Lng          *float64 `json:"lng"`
		PricePerHour *int     `json:"price_per_hour"`
		IsListed     *bool    `json:"is_listed"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	if (body.Lat == nil) != (body.Lng == nil) {
		writeError(writer, http.StatusBadRequest, "bad_request", "lat and lng move together")
		return
	}
	if body.Lat != nil && !validCoordinates(*body.Lat, *body.Lng) {
		writeError(writer, http.StatusBadRequest, "bad_request", "lat/lng out of range")
		return
	}
	if body.PricePerHour != nil && *body.PricePerHour < 0 {
		writeError(writer, http.StatusBadRequest, "bad_request", "negative price")
		return
	}
	car, err := server.params.Store.UpdateCar(request.Context(), currentUser(request).ID, request.PathValue("id"),
		store.CarPatch{Lat: body.Lat, Lng: body.Lng, PricePerHour: body.PricePerHour, IsListed: body.IsListed})
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, toCarResponse(car))
}

// handleAvailability lists cars free for the whole window, closest first by
// default, cheapest first with sort=price. The radius silently clamps to the
// configured bounds, pages are 100 cars, and searches cap at 1,000 results.
// Pages come from the snapped-and-cached search (searchcache.go); distances
// in the response are recomputed from the exact search point, so the
// snapping never shows through.
func (server *Server) handleAvailability(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	lat, latErr := strconv.ParseFloat(query.Get("lat"), 64)
	lng, lngErr := strconv.ParseFloat(query.Get("lng"), 64)
	from, fromErr := time.Parse(time.RFC3339, query.Get("from"))
	durationMinutes, durationErr := strconv.Atoi(query.Get("duration_minutes"))
	if latErr != nil || lngErr != nil || fromErr != nil || durationErr != nil || !validCoordinates(lat, lng) {
		writeError(writer, http.StatusBadRequest, "bad_request", "need lat, lng, from (RFC3339), duration_minutes")
		return
	}
	duration := time.Duration(durationMinutes) * time.Minute
	if duration < minTripDuration || duration > maxTripDuration {
		writeError(writer, http.StatusBadRequest, "bad_request", "duration must be between 15 minutes and 90 days")
		return
	}
	// The horizon bounds the cache keyspace: without it, arbitrary far-future
	// dates mint unlimited distinct cache keys and thrash the eviction cap.
	if from.Before(time.Now().Add(-pastGrace)) || from.After(time.Now().Add(searchFromHorizon)) {
		writeError(writer, http.StatusBadRequest, "bad_request", "from must be between now and a year out")
		return
	}
	page := 0
	if raw := query.Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed >= maxPages {
			writeError(writer, http.StatusBadRequest, "bad_request", "page must be 0 through 9, searches cap at 1000 results")
			return
		}
		page = parsed
	}
	rangeMeters := server.params.SearchRangeMaxMeters
	if raw := query.Get("range_meters"); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		// NaN would sail through the min/max clamp below, both propagate it.
		if err != nil || math.IsNaN(parsed) {
			writeError(writer, http.StatusBadRequest, "bad_request", "range_meters must be a number")
			return
		}
		rangeMeters = parsed
	}
	rangeMeters = min(max(rangeMeters, server.params.SearchRangeMinMeters), server.params.SearchRangeMaxMeters)
	sort := query.Get("sort")
	if sort == "" {
		sort = "distance"
	}
	if sort != "price" && sort != "distance" {
		writeError(writer, http.StatusBadRequest, "bad_request", "sort must be price or distance")
		return
	}

	snapped := snapSearch(lat, lng, rangeMeters, from, duration, sort, page)
	cached, hit, err := server.searches.Do(request.Context(), snapped.key, func() ([]store.AvailableCar, error) {
		return server.params.Store.Availability(request.Context(), store.AvailabilityParams{
			Lat: snapped.lat, Lng: snapped.lng, RangeMeters: snapped.rangeMeters,
			Start: snapped.start, End: snapped.end, Sort: sort,
			Limit: pageSize, Offset: page * pageSize,
		})
	})
	if err != nil {
		slog.Error("availability", slog.String("error", err.Error()))
		metrics.ErrorsTotal.WithLabelValues("availability", "query").Inc()
		writeStoreError(writer, err)
		return
	}
	if hit {
		metrics.SearchCacheTotal.WithLabelValues("hit").Inc()
	} else {
		metrics.SearchCacheTotal.WithLabelValues("miss").Inc()
	}
	results := personalizeSearch(cached, lat, lng, duration, sort)

	// availabilityItem is deliberately not carResponse. Search is public, and
	// who owns which car is nobody's business, so owner_id must not appear.
	// is_listed is always true here. Coordinates round to 6 decimals (~11cm)
	// and distance to whole meters: pins and "2.1km away" need no more, and
	// full-precision floats were half the serialization bill at load.
	type availabilityItem struct {
		ID             string  `json:"id"`
		Model          string  `json:"model"`
		ModelYear      *int    `json:"model_year,omitempty"`
		Lat            float64 `json:"lat"`
		Lng            float64 `json:"lng"`
		PricePerHour   int     `json:"price_per_hour"`
		TripPrice      int     `json:"trip_price"`
		DistanceMeters float64 `json:"distance_meters"`
	}
	type availabilityResponse struct {
		Cars []availabilityItem `json:"cars"`
		Page int                `json:"page"`
	}
	items := make([]availabilityItem, 0, len(results))
	for _, result := range results {
		items = append(items, availabilityItem{
			ID:             result.ID,
			Model:          result.Model,
			ModelYear:      result.ModelYear,
			Lat:            math.Round(result.Lat*1e6) / 1e6,
			Lng:            math.Round(result.Lng*1e6) / 1e6,
			PricePerHour:   result.PricePerHour,
			TripPrice:      result.TripPrice,
			DistanceMeters: math.Round(result.DistanceMeters),
		})
	}
	writeJSON(writer, http.StatusOK, availabilityResponse{Cars: items, Page: page})
}

// handleOrderCar books a car: a rental, a short pre-payment hold, or an owner
// block. Every rejection path maps to a stable error code, and the outcome
// feeds the carshare_bookings_total metric.
func (server *Server) handleOrderCar(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CarID           string    `json:"car_id"`
		Price           int       `json:"price"`
		From            time.Time `json:"from"`
		DurationMinutes int       `json:"duration_minutes"`
		Kind            string    `json:"kind"`
		IdempotencyKey  string    `json:"idempotency_key"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	duration := time.Duration(body.DurationMinutes) * time.Minute
	switch {
	case body.CarID == "":
		writeError(writer, http.StatusBadRequest, "bad_request", "car_id is required")
		return
	case body.Kind != store.KindRental && body.Kind != store.KindRentalHold && body.Kind != store.KindOwner:
		writeError(writer, http.StatusBadRequest, "bad_request", "kind must be rental, rental_hold, or owner")
		return
	case duration < minTripDuration || duration > maxTripDuration:
		writeError(writer, http.StatusBadRequest, "bad_request", "duration must be between 15 minutes and 90 days")
		return
	case body.From.Before(time.Now().Add(-pastGrace)):
		writeError(writer, http.StatusBadRequest, "bad_request", "from is in the past")
		return
	case len(body.IdempotencyKey) > 128:
		writeError(writer, http.StatusBadRequest, "bad_request", "idempotency_key too long")
		return
	}

	reservation, err := server.params.Store.OrderCar(request.Context(), store.OrderParams{
		CarID:          body.CarID,
		BookerID:       currentUser(request).ID,
		Kind:           body.Kind,
		Start:          body.From,
		End:            body.From.Add(duration),
		MaxPrice:       body.Price,
		IdempotencyKey: body.IdempotencyKey,
		HoldTTL:        server.params.HoldTTL,
	})
	if err != nil {
		metrics.BookingsTotal.WithLabelValues(body.Kind, bookingOutcome(err)).Inc()
		if !isClientBookingError(err) {
			slog.Error("order car", slog.String("car_id", body.CarID), slog.String("error", err.Error()))
			metrics.ErrorsTotal.WithLabelValues("order", "store").Inc()
		}
		writeStoreError(writer, err)
		return
	}
	metrics.BookingsTotal.WithLabelValues(body.Kind, "confirmed").Inc()
	writeJSON(writer, http.StatusCreated, toReservationResponse(reservation))
}

// handleConfirmHold turns the caller's live hold into a rental.
func (server *Server) handleConfirmHold(writer http.ResponseWriter, request *http.Request) {
	reservation, err := server.params.Store.ConfirmHold(request.Context(), request.PathValue("id"), currentUser(request).ID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, toReservationResponse(reservation))
}

// handleCancelReservation cancels the caller's booking: free up to 24 hours
// before start, or within one hour of booking. Holds cancel any time.
func (server *Server) handleCancelReservation(writer http.ResponseWriter, request *http.Request) {
	err := server.params.Store.CancelReservation(request.Context(), request.PathValue("id"), currentUser(request).ID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// handleMyCars lists the caller's listed cars, hidden ones included.
func (server *Server) handleMyCars(writer http.ResponseWriter, request *http.Request) {
	cars, err := server.params.Store.CarsByOwner(request.Context(), currentUser(request).ID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	items := make([]carResponse, 0, len(cars))
	for _, car := range cars {
		items = append(items, toCarResponse(car))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"cars": items})
}

// handleMyReservations lists the caller's bookings, upcoming first.
func (server *Server) handleMyReservations(writer http.ResponseWriter, request *http.Request) {
	reservations, err := server.params.Store.MyReservations(request.Context(), currentUser(request).ID)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	items := make([]reservationResponse, 0, len(reservations))
	for _, reservation := range reservations {
		items = append(items, toReservationResponse(reservation))
	}
	writeJSON(writer, http.StatusOK, map[string]any{"reservations": items})
}

// handleCarCalendar shows the owner their car's bookings in a window plus its
// recurring holds. Owners only.
func (server *Server) handleCarCalendar(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	from, fromErr := time.Parse(time.RFC3339, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339, query.Get("to"))
	if fromErr != nil || toErr != nil || !to.After(from) {
		writeError(writer, http.StatusBadRequest, "bad_request", "need from and to (RFC3339), to after from")
		return
	}
	calendar, err := server.params.Store.CarCalendar(request.Context(), request.PathValue("id"), currentUser(request).ID, from, to)
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	reservations := make([]reservationResponse, 0, len(calendar.Reservations))
	for _, reservation := range calendar.Reservations {
		reservations = append(reservations, toReservationResponse(reservation))
	}
	type scheduleResponse struct {
		ID         string    `json:"id"`
		FirstStart time.Time `json:"first_start"`
		FirstEnd   time.Time `json:"first_end"`
		Period     string    `json:"period"`
		Timezone   string    `json:"timezone"`
	}
	schedules := make([]scheduleResponse, 0, len(calendar.Recurrences))
	for _, recurrence := range calendar.Recurrences {
		schedules = append(schedules, scheduleResponse{ID: recurrence.ID, FirstStart: recurrence.FirstStart,
			FirstEnd: recurrence.FirstEnd, Period: recurrence.Period, Timezone: recurrence.Timezone})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"reservations": reservations, "schedules": schedules})
}

// periodFloor is the shortest possible gap between two occurrences of each
// period, used to reject occurrences longer than their own repeat cycle.
var periodFloor = map[string]time.Duration{
	"weekly":  7 * 24 * time.Hour,
	"monthly": 28 * 24 * time.Hour,
	"yearly":  365 * 24 * time.Hour,
}

// handleCreateSchedule adds a repeating owner hold, like every Wednesday
// 1-3pm. Existing renter bookings on future occurrences stand, the owner just
// misses those occurrences.
func (server *Server) handleCreateSchedule(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		CarID           string    `json:"car_id"`
		From            time.Time `json:"from"`
		DurationMinutes int       `json:"duration_minutes"`
		Period          string    `json:"period"`
		Timezone        string    `json:"timezone"`
	}
	if !decodeJSON(writer, request, &body) {
		return
	}
	floor, knownPeriod := periodFloor[body.Period]
	duration := time.Duration(body.DurationMinutes) * time.Minute
	if body.Timezone == "" {
		body.Timezone = "UTC"
	}
	_, timezoneErr := time.LoadLocation(body.Timezone)
	switch {
	case body.CarID == "":
		writeError(writer, http.StatusBadRequest, "bad_request", "car_id is required")
		return
	case !knownPeriod:
		writeError(writer, http.StatusBadRequest, "bad_request", "period must be weekly, monthly, or yearly")
		return
	case duration < minTripDuration || duration >= floor:
		writeError(writer, http.StatusBadRequest, "bad_request", "occurrence must be at least 15 minutes and shorter than its period")
		return
	case timezoneErr != nil:
		writeError(writer, http.StatusBadRequest, "bad_request", "unknown IANA timezone")
		return
	}
	recurrence, err := server.params.Store.CreateSchedule(request.Context(), store.ScheduleParams{
		CarID: body.CarID, OwnerID: currentUser(request).ID,
		Start: body.From, End: body.From.Add(duration),
		Period: body.Period, Timezone: body.Timezone,
	})
	if err != nil {
		writeStoreError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"id": recurrence.ID, "car_id": recurrence.CarID,
		"first_start": recurrence.FirstStart, "first_end": recurrence.FirstEnd,
		"period": recurrence.Period, "timezone": recurrence.Timezone,
	})
}

// handleDeleteSchedule cancels a repeating hold, freeing its future
// occurrences. Owners only.
func (server *Server) handleDeleteSchedule(writer http.ResponseWriter, request *http.Request) {
	if err := server.params.Store.DeactivateSchedule(request.Context(), request.PathValue("id"), currentUser(request).ID); err != nil {
		writeStoreError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func validCoordinates(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180
}

// bookingOutcome labels a failed booking for the metrics counter.
func bookingOutcome(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "conflict"
	case errors.Is(err, store.ErrPriceChanged):
		return "price_changed"
	default:
		return "rejected"
	}
}

func isClientBookingError(err error) bool {
	return errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrPriceChanged) ||
		errors.Is(err, store.ErrScheduleConflict) || errors.Is(err, store.ErrCarNotBookable) ||
		errors.Is(err, store.ErrNotFound)
}
