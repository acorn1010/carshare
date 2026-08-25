// Package metrics declares every Prometheus collector for the service. All
// metrics share the carshare_ prefix so one Grafana instance can host several
// services without name collisions.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// RequestsTotal counts HTTP requests by route pattern and status class.
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "carshare_requests_total",
		Help: "HTTP requests handled, labeled by route and status class.",
	}, []string{"route", "status"})

	// RequestDuration tracks per-route latency so alerts can watch p95.
	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "carshare_request_duration_seconds",
		Help:    "HTTP request latency by route.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"route"})

	// BookingsTotal counts booking attempts by kind and what happened to them.
	BookingsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "carshare_bookings_total",
		Help: "Booking attempts, labeled by kind (rental, rental_hold, owner) and outcome (confirmed, conflict, price_changed, rejected).",
	}, []string{"kind", "outcome"})

	// HoldsExpiredTotal counts expired holds reaped by later booking attempts.
	HoldsExpiredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "carshare_holds_expired_total",
		Help: "Expired rental holds deleted to make room for new bookings.",
	})

	// ErrorsTotal counts internal failures by component so alerts can point at
	// the broken part instead of a global error rate.
	ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "carshare_errors_total",
		Help: "Internal errors, labeled by component and kind.",
	}, []string{"component", "kind"})

	// DoubleBookedPairs is the invariant gauge. The exclusion constraint makes
	// overlap impossible, and this gauge proves it stays at zero in production.
	DoubleBookedPairs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "carshare_double_booked_pairs",
		Help: "Pairs of confirmed reservations overlapping on the same car. Must always be zero.",
	})
)

// Handler exposes the default registry for the metrics listener.
func Handler() http.Handler {
	return promhttp.Handler()
}
