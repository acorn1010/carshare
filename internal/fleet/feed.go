package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"carshare/internal/metrics"
)

const (
	// pollInterval is the fallback pull cadence. With a realtime wake wired
	// the poll almost never finds anything, without one it bounds staleness.
	pollInterval = 250 * time.Millisecond
	// stabilityLag is how old a log row must be before the cursor moves past
	// it. Sequences commit out of order (a slow transaction can commit a low
	// seq after a fast one committed a higher seq), so the cursor trails by
	// this much and the tail is re-applied each pull. Applies are idempotent,
	// so the overlap costs nothing but a few map writes.
	stabilityLag = 2 * time.Second
	// logRetention is how long fleet_log rows are kept. A pod further behind
	// than this rebuilds from a snapshot anyway.
	logRetention = 24 * time.Hour
)

// entry is one cars.fleet_log row, shaped by cars.log_fleet_change().
type entry struct {
	Table         string     `json:"t"`
	Op            string     `json:"op"`
	ID            string     `json:"id"`
	CarID         string     `json:"car_id"`
	Model         string     `json:"model"`
	ModelYear     *int       `json:"model_year"`
	Lat           float64    `json:"lat"`
	Lng           float64    `json:"lng"`
	PricePerHour  int        `json:"price_per_hour"`
	IsListed      bool       `json:"is_listed"`
	Start         *time.Time `json:"start"`
	End           *time.Time `json:"end"`
	Status        string     `json:"status"`
	HoldExpiresAt *time.Time `json:"hold_expires_at"`
	FirstStart    *time.Time `json:"first_start"`
	FirstEnd      *time.Time `json:"first_end"`
	Period        string     `json:"period"`
	Timezone      string     `json:"timezone"`
	Active        bool       `json:"active"`
}

// Wake is a signal that the log probably has new rows, so the next pull
// happens now instead of at the next tick. Tests use it for fast
// convergence, and a push feed (foony.io Database Sync watching fleet_log)
// can plug in later without touching the loop. Nil is fine: the poll alone
// bounds staleness. Sends never block.
type Wake chan struct{}

func (wake Wake) Poke() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// Start builds the fleet from a snapshot, blocking until it is ready, then
// follows cars.fleet_log until ctx ends. On any feed error it rebuilds from a
// fresh snapshot while the previous model keeps serving searches.
func Start(ctx context.Context, pool *pgxpool.Pool, wake Wake) (*Fleet, error) {
	fleet := &Fleet{model: newModel()}
	if err := fleet.rebuild(ctx, pool); err != nil {
		return nil, err
	}
	go func() {
		for ctx.Err() == nil {
			if err := fleet.follow(ctx, pool, wake); err != nil && ctx.Err() == nil {
				slog.Error("fleet feed", slog.String("error", err.Error()))
				metrics.FleetRebuildsTotal.Inc()
				time.Sleep(time.Second)
				if err := fleet.rebuild(ctx, pool); err != nil {
					slog.Error("fleet rebuild", slog.String("error", err.Error()))
				}
			}
		}
	}()
	return fleet, nil
}

// rebuild loads a whole model in one repeatable-read snapshot and swaps it
// in. The cursor starts at the newest log row already stable at snapshot
// time, so the follow loop re-applies anything the snapshot raced with.
func (fleet *Fleet) rebuild(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("fleet: begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lastSeq int64
	err = tx.QueryRow(ctx, `
		SELECT coalesce(max(seq), 0) FROM cars.fleet_log
		WHERE created_at < now() - $1::interval`, stabilityLag.String()).Scan(&lastSeq)
	if err != nil {
		return fmt.Errorf("fleet: snapshot cursor: %w", err)
	}

	fresh := newModel()
	rows, err := tx.Query(ctx, `
		SELECT id, model, model_year, location[1], location[0], price_per_hour, is_listed
		FROM cars.cars`)
	if err != nil {
		return fmt.Errorf("fleet: snapshot cars: %w", err)
	}
	for rows.Next() {
		loaded := &car{}
		if err := rows.Scan(&loaded.id, &loaded.model, &loaded.modelYear,
			&loaded.lat, &loaded.lng, &loaded.pricePerHour, &loaded.listed); err != nil {
			rows.Close()
			return fmt.Errorf("fleet: snapshot car scan: %w", err)
		}
		fresh.upsertCar(loaded)
	}
	rows.Close()
	if rows.Err() != nil {
		return fmt.Errorf("fleet: snapshot cars: %w", rows.Err())
	}

	rows, err = tx.Query(ctx, `
		SELECT id, car_id, lower(during), upper(during), hold_expires_at
		FROM cars.reservations
		WHERE status = 'confirmed' AND upper(during) > now()`)
	if err != nil {
		return fmt.Errorf("fleet: snapshot reservations: %w", err)
	}
	for rows.Next() {
		var booked window
		var carID string
		if err := rows.Scan(&booked.id, &carID, &booked.start, &booked.end, &booked.holdExpiresAt); err != nil {
			rows.Close()
			return fmt.Errorf("fleet: snapshot reservation scan: %w", err)
		}
		fresh.upsertWindow(carID, booked)
	}
	rows.Close()
	if rows.Err() != nil {
		return fmt.Errorf("fleet: snapshot reservations: %w", rows.Err())
	}

	rows, err = tx.Query(ctx, `
		SELECT id, car_id, lower(first_occurrence), upper(first_occurrence), period, timezone
		FROM cars.recurrences WHERE active`)
	if err != nil {
		return fmt.Errorf("fleet: snapshot recurrences: %w", err)
	}
	for rows.Next() {
		var schedule recurrence
		var carID, timezone string
		if err := rows.Scan(&schedule.id, &carID, &schedule.firstStart, &schedule.firstEnd,
			&schedule.period, &timezone); err != nil {
			rows.Close()
			return fmt.Errorf("fleet: snapshot recurrence scan: %w", err)
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			location = time.UTC
		}
		schedule.location = location
		fresh.upsertRecurrence(carID, schedule)
	}
	rows.Close()
	if rows.Err() != nil {
		return fmt.Errorf("fleet: snapshot recurrences: %w", rows.Err())
	}

	fleet.mu.Lock()
	fleet.model = fresh
	fleet.lastSeq = lastSeq
	fleet.mu.Unlock()
	metrics.FleetCars.Set(float64(len(fresh.cars)))
	slog.Info("fleet snapshot loaded", slog.Int("cars", len(fresh.cars)), slog.Int64("seq", lastSeq))
	return nil
}

// follow pulls new log rows until ctx ends or the database errors.
func (fleet *Fleet) follow(ctx context.Context, pool *pgxpool.Pool, wake Wake) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	lastPrune := time.Now()
	for {
		if err := fleet.pull(ctx, pool); err != nil {
			return err
		}
		if time.Since(lastPrune) > time.Hour {
			lastPrune = time.Now()
			if _, err := pool.Exec(ctx, `DELETE FROM cars.fleet_log WHERE created_at < now() - $1::interval`,
				logRetention.String()); err != nil {
				return fmt.Errorf("fleet: prune log: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-wake:
		}
	}
}

// pull applies every log row past the cursor, then advances the cursor only
// past rows older than stabilityLag, so out-of-order commits are re-read
// rather than skipped.
func (fleet *Fleet) pull(ctx context.Context, pool *pgxpool.Pool) error {
	fleet.mu.RLock()
	cursor := fleet.lastSeq
	fleet.mu.RUnlock()

	rows, err := pool.Query(ctx, `
		SELECT seq, entry, created_at < now() - $2::interval
		FROM cars.fleet_log WHERE seq > $1 ORDER BY seq`,
		cursor, stabilityLag.String())
	if err != nil {
		return fmt.Errorf("fleet: pull: %w", err)
	}
	defer rows.Close()

	type pulled struct {
		seq    int64
		raw    []byte
		stable bool
	}
	var batch []pulled
	for rows.Next() {
		var row pulled
		if err := rows.Scan(&row.seq, &row.raw, &row.stable); err != nil {
			return fmt.Errorf("fleet: pull scan: %w", err)
		}
		batch = append(batch, row)
	}
	if rows.Err() != nil {
		return fmt.Errorf("fleet: pull rows: %w", rows.Err())
	}
	if len(batch) == 0 {
		return nil
	}

	now := time.Now()
	fleet.mu.Lock()
	for _, row := range batch {
		var change entry
		if err := json.Unmarshal(row.raw, &change); err != nil {
			slog.Error("fleet entry", slog.Int64("seq", row.seq), slog.String("error", err.Error()))
			continue
		}
		fleet.model.apply(change, now)
		if row.stable {
			fleet.lastSeq = row.seq
		}
	}
	carCount := len(fleet.model.cars)
	fleet.mu.Unlock()
	metrics.FleetChangesTotal.Add(float64(len(batch)))
	metrics.FleetCars.Set(float64(carCount))
	return nil
}

// apply folds one change entry into the model. Applies are idempotent, the
// feed re-delivers the unstable tail on every pull.
func (m *model) apply(change entry, now time.Time) {
	switch change.Table {
	case "car":
		if change.Op == "DELETE" {
			m.removeCar(change.ID)
			return
		}
		existing := m.cars[change.ID]
		if existing == nil {
			existing = &car{id: change.ID}
		}
		existing.model = change.Model
		existing.modelYear = change.ModelYear
		existing.lat, existing.lng = change.Lat, change.Lng
		existing.pricePerHour = change.PricePerHour
		existing.listed = change.IsListed
		m.upsertCar(existing)
	case "reservation":
		if change.Op == "DELETE" || change.Status != "confirmed" ||
			change.End == nil || !change.End.After(now) {
			m.removeWindow(change.ID)
			return
		}
		m.upsertWindow(change.CarID, window{
			id: change.ID, start: *change.Start, end: *change.End, holdExpiresAt: change.HoldExpiresAt,
		})
	case "recurrence":
		if change.Op == "DELETE" || !change.Active || change.FirstStart == nil || change.FirstEnd == nil {
			m.removeRecurrence(change.ID)
			return
		}
		location, err := time.LoadLocation(change.Timezone)
		if err != nil {
			location = time.UTC
		}
		m.upsertRecurrence(change.CarID, recurrence{
			id: change.ID, firstStart: *change.FirstStart, firstEnd: *change.FirstEnd,
			period: change.Period, location: location,
		})
	}
}

func (m *model) upsertCar(loaded *car) {
	cell := cellOf(loaded.lat, loaded.lng)
	if existing, ok := m.cars[loaded.id]; ok && existing.cell != cell {
		delete(m.cells[existing.cell], loaded.id)
	}
	loaded.cell = cell
	m.cars[loaded.id] = loaded
	bucket, ok := m.cells[cell]
	if !ok {
		bucket = make(map[string]*car)
		m.cells[cell] = bucket
	}
	bucket[loaded.id] = loaded
}

func (m *model) removeCar(id string) {
	existing, ok := m.cars[id]
	if !ok {
		return
	}
	delete(m.cells[existing.cell], id)
	delete(m.cars, id)
}

func (m *model) upsertWindow(carID string, booked window) {
	owner, ok := m.cars[carID]
	if !ok {
		return
	}
	m.reservationCar[booked.id] = carID
	for i := range owner.busy {
		if owner.busy[i].id == booked.id {
			owner.busy[i] = booked
			return
		}
	}
	owner.busy = append(owner.busy, booked)
}

func (m *model) removeWindow(reservationID string) {
	carID, ok := m.reservationCar[reservationID]
	if !ok {
		return
	}
	delete(m.reservationCar, reservationID)
	owner, ok := m.cars[carID]
	if !ok {
		return
	}
	for i := range owner.busy {
		if owner.busy[i].id == reservationID {
			owner.busy = append(owner.busy[:i], owner.busy[i+1:]...)
			return
		}
	}
}

func (m *model) upsertRecurrence(carID string, schedule recurrence) {
	owner, ok := m.cars[carID]
	if !ok {
		return
	}
	m.recurrenceCar[schedule.id] = carID
	for i := range owner.recurrences {
		if owner.recurrences[i].id == schedule.id {
			owner.recurrences[i] = schedule
			return
		}
	}
	owner.recurrences = append(owner.recurrences, schedule)
}

func (m *model) removeRecurrence(recurrenceID string) {
	carID, ok := m.recurrenceCar[recurrenceID]
	if !ok {
		return
	}
	delete(m.recurrenceCar, recurrenceID)
	owner, ok := m.cars[carID]
	if !ok {
		return
	}
	for i := range owner.recurrences {
		if owner.recurrences[i].id == recurrenceID {
			owner.recurrences = append(owner.recurrences[:i], owner.recurrences[i+1:]...)
			return
		}
	}
}
