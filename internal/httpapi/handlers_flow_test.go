// Flow tests drive the whole HTTP surface against memstore and a stub OAuth
// provider: sign in, list a car, search, hold, confirm, conflict, cancel,
// schedule. No database or network needed.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"carshare/internal/auth"
	"carshare/internal/httpapi"
	"carshare/internal/store"
	"carshare/internal/store/memstore"
)

type fakeProvider struct {
	profiles map[string]auth.Profile
}

func (provider fakeProvider) AuthCodeURL(state string) string {
	return "https://accounts.example/auth?state=" + url.QueryEscape(state)
}

func (provider fakeProvider) FetchProfile(_ context.Context, code string) (auth.Profile, error) {
	profile, ok := provider.profiles[code]
	if !ok {
		return auth.Profile{}, fmt.Errorf("unknown code %q", code)
	}
	return profile, nil
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestServerWithStore(t, memstore.New())
}

func newTestServerWithStore(t *testing.T, dataStore store.DataStore) *httptest.Server {
	t.Helper()
	server := httpapi.NewServer(httpapi.Params{
		Store: dataStore,
		Google: fakeProvider{profiles: map[string]auth.Profile{
			"code-owner":  {Sub: "sub-owner", Email: "owner@example.com", Name: "Owner"},
			"code-renter": {Sub: "sub-renter", Email: "renter@example.com", Name: "Renter"},
			"code-other":  {Sub: "sub-other", Email: "other@example.com", Name: "Other"},
		}},
		SessionTTL:           time.Hour,
		HoldTTL:              10 * time.Minute,
		SearchRangeMinMeters: 100,
		SearchRangeMaxMeters: 50_000,
	})
	testServer := httptest.NewServer(server.Routes())
	t.Cleanup(testServer.Close)
	return testServer
}

// signIn walks the OAuth dance with the stub provider and returns a client
// whose cookie jar holds the session.
func signIn(t *testing.T, testServer *httptest.Server, code string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("jar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginResponse, err := client.Get(testServer.URL + "/v1/auth/google/login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	_ = loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusFound {
		t.Fatalf("login status = %d", loginResponse.StatusCode)
	}
	location, err := url.Parse(loginResponse.Header.Get("Location"))
	if err != nil {
		t.Fatalf("login redirect: %v", err)
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect")
	}

	callbackResponse, err := client.Get(testServer.URL + "/v1/auth/google/callback?state=" + url.QueryEscape(state) + "&code=" + code)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	_ = callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d", callbackResponse.StatusCode)
	}
	return client
}

func doJSON(t *testing.T, client *http.Client, method, target string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return response.StatusCode, decoded
}

func TestUnauthenticatedIsRejected(t *testing.T) {
	testServer := newTestServer(t)
	status, body := doJSON(t, http.DefaultClient, http.MethodGet, testServer.URL+"/v1/me", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %v", status, body)
	}
}

func TestOAuthSignInAndMe(t *testing.T) {
	testServer := newTestServer(t)
	client := signIn(t, testServer, "code-owner")

	status, body := doJSON(t, client, http.MethodGet, testServer.URL+"/v1/me", nil)
	if status != http.StatusOK || body["email"] != "owner@example.com" {
		t.Fatalf("me = %d %v", status, body)
	}

	// State mismatch is rejected.
	response, err := client.Get(testServer.URL + "/v1/auth/google/callback?state=wrong&code=code-owner")
	if err != nil {
		t.Fatalf("bad state: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad state status = %d", response.StatusCode)
	}

	// Logout kills the session.
	if status, _ := doJSON(t, client, http.MethodPost, testServer.URL+"/v1/auth/logout", nil); status != http.StatusNoContent {
		t.Fatalf("logout = %d", status)
	}
	if status, _ := doJSON(t, client, http.MethodGet, testServer.URL+"/v1/me", nil); status != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d", status)
	}
}

func TestBookingLifecycleOverHTTP(t *testing.T) {
	testServer := newTestServer(t)
	owner := signIn(t, testServer, "code-owner")
	renter := signIn(t, testServer, "code-renter")
	other := signIn(t, testServer, "code-other")

	status, car := doJSON(t, owner, http.MethodPost, testServer.URL+"/v1/cars",
		map[string]any{"lat": 37.77, "lng": -122.42, "price_per_hour": 1500})
	if status != http.StatusCreated {
		t.Fatalf("create car = %d %v", status, car)
	}
	carID := car["id"].(string)

	from := time.Now().Add(48 * time.Hour).Truncate(time.Minute).UTC()
	search := fmt.Sprintf("%s/v1/availability?lat=37.771&lng=-122.42&from=%s&duration_minutes=120",
		testServer.URL, url.QueryEscape(from.Format(time.RFC3339)))
	status, results := doJSON(t, renter, http.MethodGet, search, nil)
	if status != http.StatusOK {
		t.Fatalf("availability = %d %v", status, results)
	}
	cars := results["cars"].([]any)
	if len(cars) != 1 {
		t.Fatalf("availability found %d cars, want 1", len(cars))
	}
	tripPrice := int(cars[0].(map[string]any)["trip_price"].(float64))
	if tripPrice != 3000 {
		t.Fatalf("trip price = %d, want 3000", tripPrice)
	}

	// Hold, then confirm.
	status, hold := doJSON(t, renter, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": tripPrice, "from": from, "duration_minutes": 120, "kind": "rental_hold",
	})
	if status != http.StatusCreated {
		t.Fatalf("hold = %d %v", status, hold)
	}
	holdID := hold["id"].(string)
	if hold["hold_expires_at"] == nil {
		t.Fatal("hold has no expiry")
	}
	status, confirmed := doJSON(t, renter, http.MethodPost, testServer.URL+"/v1/reservations/"+holdID+"/confirm", nil)
	if status != http.StatusOK || confirmed["kind"] != "rental" {
		t.Fatalf("confirm = %d %v", status, confirmed)
	}

	// A second renter hits 409 on the same window.
	status, conflict := doJSON(t, other, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": tripPrice, "from": from, "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusConflict {
		t.Fatalf("conflict = %d %v", status, conflict)
	}

	// The contiguous slot right after is free.
	status, _ = doJSON(t, other, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": tripPrice, "from": from.Add(2 * time.Hour), "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusCreated {
		t.Fatalf("contiguous booking = %d", status)
	}

	// Stale price is refused with 409 price_changed.
	status, priced := doJSON(t, other, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": 1, "from": from.Add(6 * time.Hour), "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusConflict || priced["error"].(map[string]any)["code"] != "price_changed" {
		t.Fatalf("price change = %d %v", status, priced)
	}

	// Cancel the far-future booking, then the slot books again.
	status, _ = doJSON(t, renter, http.MethodDelete, testServer.URL+"/v1/reservations/"+holdID, nil)
	if status != http.StatusNoContent {
		t.Fatalf("cancel = %d", status)
	}
	status, _ = doJSON(t, other, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": tripPrice, "from": from, "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusCreated {
		t.Fatalf("rebook after cancel = %d", status)
	}

	status, mine := doJSON(t, other, http.MethodGet, testServer.URL+"/v1/me/reservations", nil)
	if status != http.StatusOK || len(mine["reservations"].([]any)) != 2 {
		t.Fatalf("my reservations = %d %v", status, mine)
	}
}

func TestSchedulesOverHTTP(t *testing.T) {
	testServer := newTestServer(t)
	owner := signIn(t, testServer, "code-owner")
	renter := signIn(t, testServer, "code-renter")

	_, car := doJSON(t, owner, http.MethodPost, testServer.URL+"/v1/cars",
		map[string]any{"lat": 37.77, "lng": -122.42, "price_per_hour": 1000})
	carID := car["id"].(string)

	first := time.Now().Add(24 * time.Hour).Truncate(time.Minute).UTC()
	status, schedule := doJSON(t, owner, http.MethodPost, testServer.URL+"/v1/schedules", map[string]any{
		"car_id": carID, "from": first, "duration_minutes": 120, "period": "weekly", "timezone": "America/Los_Angeles",
	})
	if status != http.StatusCreated {
		t.Fatalf("schedule = %d %v", status, schedule)
	}
	scheduleID := schedule["id"].(string)

	// A renter cannot schedule someone else's car.
	status, _ = doJSON(t, renter, http.MethodPost, testServer.URL+"/v1/schedules", map[string]any{
		"car_id": carID, "from": first, "duration_minutes": 60, "period": "weekly",
	})
	if status != http.StatusNotFound {
		t.Fatalf("renter schedule = %d", status)
	}

	// Booking on a future occurrence is blocked.
	occurrence := first.Add(2 * 7 * 24 * time.Hour)
	status, blocked := doJSON(t, renter, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": 2000, "from": occurrence, "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusConflict || blocked["error"].(map[string]any)["code"] != "owner_schedule_conflict" {
		t.Fatalf("blocked = %d %v", status, blocked)
	}

	// The owner sees it on the calendar, the renter cannot look.
	calendarURL := fmt.Sprintf("%s/v1/cars/%s/calendar?from=%s&to=%s", testServer.URL, carID,
		url.QueryEscape(first.Add(-time.Hour).Format(time.RFC3339)),
		url.QueryEscape(first.Add(21*24*time.Hour).Format(time.RFC3339)))
	status, calendar := doJSON(t, owner, http.MethodGet, calendarURL, nil)
	if status != http.StatusOK || len(calendar["schedules"].([]any)) != 1 {
		t.Fatalf("calendar = %d %v", status, calendar)
	}
	if status, _ := doJSON(t, renter, http.MethodGet, calendarURL, nil); status != http.StatusNotFound {
		t.Fatalf("renter calendar = %d", status)
	}

	// Delete frees the occurrence.
	if status, _ := doJSON(t, owner, http.MethodDelete, testServer.URL+"/v1/schedules/"+scheduleID, nil); status != http.StatusNoContent {
		t.Fatalf("delete schedule = %d", status)
	}
	status, _ = doJSON(t, renter, http.MethodPost, testServer.URL+"/v1/reservations", map[string]any{
		"car_id": carID, "price": 2000, "from": occurrence, "duration_minutes": 120, "kind": "rental",
	})
	if status != http.StatusCreated {
		t.Fatalf("booking after schedule delete = %d", status)
	}
}

func TestValidationErrors(t *testing.T) {
	testServer := newTestServer(t)
	client := signIn(t, testServer, "code-owner")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown kind", map[string]any{"car_id": "x", "price": 1, "from": time.Now().Add(time.Hour), "duration_minutes": 60, "kind": "forever"}},
		{"too short", map[string]any{"car_id": "x", "price": 1, "from": time.Now().Add(time.Hour), "duration_minutes": 5, "kind": "rental"}},
		{"past start", map[string]any{"car_id": "x", "price": 1, "from": time.Now().Add(-time.Hour), "duration_minutes": 60, "kind": "rental"}},
		{"missing car", map[string]any{"price": 1, "from": time.Now().Add(time.Hour), "duration_minutes": 60, "kind": "rental"}},
		{"unknown field", map[string]any{"car_id": "x", "price": 1, "from": time.Now().Add(time.Hour), "duration_minutes": 60, "kind": "rental", "surprise": true}},
	}
	for _, testCase := range cases {
		status, _ := doJSON(t, client, http.MethodPost, testServer.URL+"/v1/reservations", testCase.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", testCase.name, status)
		}
	}

	// Occurrence longer than its period.
	status, _ := doJSON(t, client, http.MethodPost, testServer.URL+"/v1/schedules", map[string]any{
		"car_id": "x", "from": time.Now().Add(time.Hour), "duration_minutes": 8 * 24 * 60, "period": "weekly",
	})
	if status != http.StatusBadRequest {
		t.Errorf("oversize occurrence: status = %d, want 400", status)
	}

	// Search page past the 1,000-result cap.
	searchURL := fmt.Sprintf("%s/v1/availability?lat=0&lng=0&from=%s&duration_minutes=60&page=10",
		testServer.URL, url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)))
	if status, _ := doJSON(t, client, http.MethodGet, searchURL, nil); status != http.StatusBadRequest {
		t.Errorf("page cap: status = %d, want 400", status)
	}
}

// countingStore wraps a store and counts Availability calls, so cache tests
// can tell a database query from a cached page.
type countingStore struct {
	store.DataStore
	availabilityCalls atomic.Int32
}

func (counting *countingStore) Availability(ctx context.Context, params store.AvailabilityParams) ([]store.AvailableCar, error) {
	counting.availabilityCalls.Add(1)
	return counting.DataStore.Availability(ctx, params)
}

// TestAvailabilityCachingAndDefaultSort proves three things over HTTP: the
// default sort is closest-first, searchers in the same snap cell share one
// store query, and cached pages still report distances measured from each
// searcher's exact point.
func TestAvailabilityCachingAndDefaultSort(t *testing.T) {
	counting := &countingStore{DataStore: memstore.New()}
	testServer := newTestServerWithStore(t, counting)
	owner := signIn(t, testServer, "code-owner")

	// A near expensive car and a far cheap one, both free for the window.
	for _, seed := range []map[string]any{
		{"lat": 37.7702, "lng": -122.4200, "price_per_hour": 2000},
		{"lat": 37.7900, "lng": -122.4200, "price_per_hour": 500},
	} {
		if status, body := doJSON(t, owner, http.MethodPost, testServer.URL+"/v1/cars", seed); status != http.StatusCreated {
			t.Fatalf("create car = %d %v", status, body)
		}
	}

	from := time.Now().Add(48 * time.Hour).Truncate(time.Minute).UTC()
	search := func(lat, lng float64, sort string) []any {
		t.Helper()
		target := fmt.Sprintf("%s/v1/availability?lat=%.4f&lng=%.4f&from=%s&duration_minutes=120",
			testServer.URL, lat, lng, url.QueryEscape(from.Format(time.RFC3339)))
		if sort != "" {
			target += "&sort=" + sort
		}
		status, body := doJSON(t, http.DefaultClient, http.MethodGet, target, nil)
		if status != http.StatusOK {
			t.Fatalf("availability = %d %v", status, body)
		}
		cars, _ := body["cars"].([]any)
		return cars
	}
	distanceOf := func(row any) float64 { return row.(map[string]any)["distance_meters"].(float64) }
	priceOf := func(row any) int { return int(row.(map[string]any)["trip_price"].(float64)) }

	// No sort parameter: closest first, so the pricey near car leads.
	first := search(37.7701, -122.4199, "")
	if len(first) != 2 || priceOf(first[0]) != 4000 {
		t.Fatalf("default sort: want the near 4000-cent trip first, got %v", first)
	}
	// Search is public, so a row must never say who owns the car.
	for key := range first[0].(map[string]any) {
		if key == "owner_id" || key == "is_listed" {
			t.Fatalf("search row leaks %q", key)
		}
	}
	if calls := counting.availabilityCalls.Load(); calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}

	// Same snap cell, different exact point: no store query, distances
	// recomputed for the new point.
	second := search(37.7705, -122.4203, "")
	if calls := counting.availabilityCalls.Load(); calls != 1 {
		t.Fatalf("search in the same cell reached the store, calls = %d", calls)
	}
	if len(second) != 2 || distanceOf(first[0]) == distanceOf(second[0]) {
		t.Fatalf("distance not personalized: %v then %v", distanceOf(first[0]), distanceOf(second[0]))
	}

	// Cheapest sort is its own cache key and leads with the cheap car.
	cheapest := search(37.7701, -122.4199, "price")
	if calls := counting.availabilityCalls.Load(); calls != 2 {
		t.Fatalf("price sort should miss the cache, calls = %d", calls)
	}
	if len(cheapest) != 2 || priceOf(cheapest[0]) != 1000 {
		t.Fatalf("price sort: want the cheap 1000-cent trip first, got %v", cheapest)
	}

	// A search cells away misses too.
	search(37.8000, -122.4200, "")
	if calls := counting.availabilityCalls.Load(); calls != 3 {
		t.Fatalf("distant search should miss, calls = %d", calls)
	}
}
