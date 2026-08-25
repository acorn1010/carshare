// Package httpapi is the HTTP surface: routing, auth middleware, validation,
// and translating store errors into statuses. No SQL lives here.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"carshare/internal/auth"
	"carshare/internal/metrics"
	"carshare/internal/store"
)

// Params carries every dependency, read-only after construction, so the type
// is concurrency-safe and tests can assemble it with fakes.
type Params struct {
	// Store is the data layer, Postgres in production, memstore in tests.
	Store store.DataStore
	// Google is the OAuth provider. Nil disables the auth routes so the
	// service still runs locally without Google credentials.
	Google auth.Provider
	// SessionTTL is how long a sign-in lasts.
	SessionTTL time.Duration
	// HoldTTL is how long a rental_hold blocks the car.
	HoldTTL time.Duration
	// SearchRangeMinMeters and SearchRangeMaxMeters clamp availability radii.
	SearchRangeMinMeters float64
	SearchRangeMaxMeters float64
	// SecureCookies marks session cookies Secure. On in production, off for
	// plain-http local runs.
	SecureCookies bool
}

// Server holds the wired dependencies.
type Server struct {
	params Params
}

// NewServer wires the API.
func NewServer(params Params) *Server {
	return &Server{params: params}
}

// pageSize is the availability page size, and maxPages caps total results at
// 1,000 per search as specified.
const (
	pageSize = 100
	maxPages = 10
)

// Routes builds the public mux. Every route is instrumented with the pattern
// as its metrics label, so cardinality stays bounded.
func (server *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, instrument(pattern, handler))
	}

	handle("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})

	if server.params.Google != nil {
		handle("GET /v1/auth/google/login", server.handleGoogleLogin)
		handle("GET /v1/auth/google/callback", server.handleGoogleCallback)
	}
	handle("POST /v1/auth/logout", server.handleLogout)
	handle("GET /v1/me", server.requireUser(server.handleMe))

	// Browsing is public, like any marketplace. Booking needs a session.
	handle("GET /v1/availability", server.handleAvailability)
	handle("GET /v1/cars/{id}", server.handleGetCar)
	handle("POST /v1/cars", server.requireUser(server.handleCreateCar))
	handle("PATCH /v1/cars/{id}", server.requireUser(server.handleUpdateCar))
	handle("GET /v1/cars/{id}/calendar", server.requireUser(server.handleCarCalendar))
	handle("POST /v1/reservations", server.requireUser(server.handleOrderCar))
	handle("POST /v1/reservations/{id}/confirm", server.requireUser(server.handleConfirmHold))
	handle("DELETE /v1/reservations/{id}", server.requireUser(server.handleCancelReservation))
	handle("GET /v1/me/reservations", server.requireUser(server.handleMyReservations))
	handle("GET /v1/me/cars", server.requireUser(server.handleMyCars))
	handle("POST /v1/schedules", server.requireUser(server.handleCreateSchedule))
	handle("DELETE /v1/schedules/{id}", server.requireUser(server.handleDeleteSchedule))

	return mux
}

type contextKey string

// userKey carries the authenticated store.User through the request context.
const userKey contextKey = "user"

// requireUser resolves the session cookie or bearer token to a user, or
// answers 401.
func (server *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		token := bearerOrCookieToken(request)
		if token == "" {
			writeError(writer, http.StatusUnauthorized, "unauthenticated", "sign in first")
			return
		}
		user, err := server.params.Store.UserBySessionToken(request.Context(), auth.HashToken(token))
		if err != nil {
			writeError(writer, http.StatusUnauthorized, "unauthenticated", "session expired or unknown")
			return
		}
		next(writer, request.WithContext(context.WithValue(request.Context(), userKey, user)))
	}
}

func currentUser(request *http.Request) store.User {
	user, _ := request.Context().Value(userKey).(store.User)
	return user
}

// sessionCookieName is shared by the auth handlers and the middleware.
const sessionCookieName = "carshare_session"

func bearerOrCookieToken(request *http.Request) string {
	header := request.Header.Get("Authorization")
	if len(header) > 7 && header[:7] == "Bearer " {
		return header[7:]
	}
	if cookie, err := request.Cookie(sessionCookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// instrument wraps a handler with the request counter and latency histogram.
func instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		metrics.RequestsTotal.WithLabelValues(route, statusClass(recorder.status)).Inc()
		metrics.RequestDuration.WithLabelValues(route).Observe(time.Since(started).Seconds())
	})
}
