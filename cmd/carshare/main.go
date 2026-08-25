// Command carshare runs the reservation API: a public listener on PORT and an
// admin listener on METRICS_PORT with /metrics and /health for Prometheus and
// probes.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	// The scratch container image has no tzdata, and recurrence validation
	// needs real IANA zones.
	_ "time/tzdata"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"carshare/internal/auth"
	"carshare/internal/config"
	"carshare/internal/fleet"
	"carshare/internal/httpapi"
	"carshare/internal/logging"
	"carshare/internal/metrics"
	"carshare/internal/store"
)

func main() {
	cfg := config.Load()
	logger := logging.New(cfg.LogLevel)
	logging.SetDefault(logger)

	if err := cfg.Require("DATABASE_URL"); err != nil {
		logger.Error("config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	connectCtx, connectCancel := context.WithTimeout(rootCtx, 10*time.Second)
	defer connectCancel()
	dataStore, err := store.New(connectCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer dataStore.Close()

	registerPoolMetrics(dataStore)
	go invariantLoop(rootCtx, dataStore, cfg.InvariantCheckInterval, logger)

	// The fleet answers every search from memory, following cars.fleet_log
	// on a short poll. Boot blocks on its first snapshot so the server never
	// serves an empty fleet.
	searchFleet, err := fleet.Start(rootCtx, dataStore.Pool(), nil)
	if err != nil {
		logger.Error("fleet", slog.String("error", err.Error()))
		os.Exit(1)
	}

	server := httpapi.NewServer(httpapi.Params{
		Store:                dataStore,
		Search:               searchFleet,
		Google:               googleProvider(logger),
		SessionTTL:           30 * 24 * time.Hour,
		HoldTTL:              cfg.HoldTTL,
		SearchRangeMinMeters: cfg.SearchRangeMinMeters,
		SearchRangeMaxMeters: cfg.SearchRangeMaxMeters,
		SecureCookies:        os.Getenv("INSECURE_COOKIES") != "1",
	})

	publicServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", metrics.Handler())
	metricsMux.HandleFunc("/health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})
	metricsServer := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: metricsMux, ReadHeaderTimeout: 10 * time.Second}

	errCh := make(chan error, 2)
	go serve("public", publicServer, logger, errCh)
	go serve("metrics", metricsServer, logger, errCh)
	logger.Info("carshare up", slog.String("port", cfg.Port), slog.String("metrics_port", cfg.MetricsPort))

	select {
	case <-rootCtx.Done():
		logger.Info("shutdown requested")
	case err := <-errCh:
		if err != nil {
			logger.Error("listener failed", slog.String("error", err.Error()))
		}
	}

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = publicServer.Shutdown(shutdownCtx)
	_ = metricsServer.Shutdown(shutdownCtx)
}

// googleProvider builds the OAuth provider, or nil when credentials are
// absent so local runs degrade instead of failing startup.
func googleProvider(logger *slog.Logger) auth.Provider {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	redirectURL := os.Getenv("OAUTH_REDIRECT_URL")
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		logger.Warn("google oauth disabled: set GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, OAUTH_REDIRECT_URL")
		return nil
	}
	return auth.NewGoogle(clientID, clientSecret, redirectURL)
}

// invariantLoop exports the double-booking invariant. The exclusion constraint
// makes overlap impossible, this loop is the running proof.
func invariantLoop(ctx context.Context, dataStore *store.Store, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pairs, err := dataStore.CountDoubleBookedPairs(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.Error("invariant check", slog.String("error", err.Error()))
					metrics.ErrorsTotal.WithLabelValues("invariant", "query").Inc()
				}
				continue
			}
			metrics.DoubleBookedPairs.Set(float64(pairs))
			if pairs > 0 {
				logger.Error("DOUBLE BOOKING DETECTED", slog.Int("pairs", pairs))
			}
		}
	}
}

// registerPoolMetrics exports pgxpool health so saturation is visible before
// it becomes latency.
func registerPoolMetrics(dataStore *store.Store) {
	pool := dataStore.Pool()
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "carshare_db_pool_in_use", Help: "Connections currently checked out.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "carshare_db_pool_idle", Help: "Idle connections in the pool.",
	}, func() float64 { return float64(pool.Stat().IdleConns()) })
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "carshare_db_pool_max", Help: "Pool size limit.",
	}, func() float64 { return float64(pool.Stat().MaxConns()) })
	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "carshare_db_pool_empty_acquires_total", Help: "Acquires that had to wait for a free connection.",
	}, func() float64 { return float64(pool.Stat().EmptyAcquireCount()) })
}

func serve(name string, server *http.Server, logger *slog.Logger, errCh chan<- error) {
	err := server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		errCh <- err
		return
	}
	logger.Info("listener closed", slog.String("name", name))
	errCh <- nil
}
