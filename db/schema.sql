CREATE EXTENSION IF NOT EXISTS "btree_gist";

CREATE SCHEMA "cars";

CREATE OR REPLACE FUNCTION "cars"."recurrence_overlaps"(first_occurrence tstzrange, repeat_period text, tz text, win tstzrange) RETURNS boolean
    LANGUAGE sql STABLE
    AS $$
-- True when any occurrence of a recurring owner hold overlaps the window.
-- Occurrence times are computed in the recurrence's timezone so a Wednesday
-- 1pm hold stays at 1pm wall-clock across DST. Closed-form: it estimates
-- which occurrence lands near the window start, then checks that occurrence
-- and its neighbors, so cost is constant per recurrence.
WITH rec AS (
    SELECT
        lower(first_occurrence) AT TIME ZONE tz AS local_start,
        upper(first_occurrence) - lower(first_occurrence) AS duration,
        lower(win) AT TIME ZONE tz AS local_win_start,
        CASE repeat_period
            WHEN 'weekly' THEN interval '7 days'
            WHEN 'monthly' THEN interval '1 month'
            ELSE interval '1 year'
        END AS step
), guess AS (
    SELECT rec.*,
        CASE repeat_period
            WHEN 'weekly' THEN floor(extract(epoch FROM rec.local_win_start - rec.local_start) / 604800)::bigint
            WHEN 'monthly' THEN (12 * (extract(year FROM rec.local_win_start) - extract(year FROM rec.local_start))
                + (extract(month FROM rec.local_win_start) - extract(month FROM rec.local_start)))::bigint
            ELSE (extract(year FROM rec.local_win_start) - extract(year FROM rec.local_start))::bigint
        END AS n0
    FROM rec
)
SELECT EXISTS (
    SELECT 1
    FROM guess, generate_series(greatest(guess.n0 - 1, 0), greatest(guess.n0 + 2, 0)) AS occurrence(n)
    WHERE tstzrange(
        (guess.local_start + occurrence.n * guess.step) AT TIME ZONE tz,
        ((guess.local_start + occurrence.n * guess.step) AT TIME ZONE tz) + guess.duration
    ) && win
);
$$;

CREATE TABLE "cars"."users" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "email" text,
    "display_name" text NOT NULL,
    "avatar_url" text,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_users_pkey PRIMARY KEY ("id")
);

CREATE TABLE "cars"."identities" (
    "provider" text NOT NULL,
    "subject" text NOT NULL,
    "user_id" uuid NOT NULL,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_identities_pkey PRIMARY KEY ("provider", "subject"),
    CONSTRAINT cars_identities_provider_check CHECK ("provider" IN ('google'))
);

CREATE INDEX cars_identities_user_index ON cars.identities USING btree (user_id);

CREATE TABLE "cars"."sessions" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "user_id" uuid NOT NULL,
    "token_hash" text NOT NULL,
    "expires_at" timestamp with time zone NOT NULL,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_sessions_pkey PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX cars_sessions_token_hash_uindex ON cars.sessions USING btree (token_hash);
CREATE INDEX cars_sessions_user_index ON cars.sessions USING btree (user_id);

CREATE TABLE "cars"."cars" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "owner_id" uuid NOT NULL,
    "model" text NOT NULL DEFAULT '',
    "model_year" integer,
    "location" point NOT NULL,
    "price_per_hour" integer NOT NULL,
    "is_listed" boolean NOT NULL DEFAULT true,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_cars_pkey PRIMARY KEY ("id"),
    CONSTRAINT cars_cars_price_per_hour_check CHECK ("price_per_hour" >= 0)
);

CREATE INDEX cars_cars_location_index ON cars.cars USING gist (location) WHERE is_listed;

CREATE TABLE "cars"."reservations" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "car_id" uuid NOT NULL,
    "booker_id" uuid NOT NULL,
    "kind" text NOT NULL,
    "during" tstzrange NOT NULL,
    "status" text NOT NULL DEFAULT 'confirmed',
    "price" integer,
    "hold_expires_at" timestamp with time zone,
    "idempotency_key" text,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_reservations_pkey PRIMARY KEY ("id"),
    CONSTRAINT cars_reservations_kind_check CHECK ("kind" IN ('rental', 'rental_hold', 'owner')),
    CONSTRAINT cars_reservations_status_check CHECK ("status" IN ('confirmed', 'cancelled')),
    CONSTRAINT cars_reservations_hold_expiry_check CHECK (("kind" = 'rental_hold') = ("hold_expires_at" IS NOT NULL)),
    CONSTRAINT cars_reservations_no_overlap EXCLUDE USING gist ("car_id" WITH =, "during" WITH &&) WHERE ("status" = 'confirmed')
);

CREATE INDEX cars_reservations_booker_index ON cars.reservations USING btree (booker_id);
CREATE UNIQUE INDEX cars_reservations_idempotency_uindex ON cars.reservations USING btree (booker_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE "cars"."recurrences" (
    "id" uuid NOT NULL DEFAULT gen_random_uuid(),
    "car_id" uuid NOT NULL,
    "first_occurrence" tstzrange NOT NULL,
    "period" text NOT NULL,
    "timezone" text NOT NULL,
    "active" boolean NOT NULL DEFAULT true,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_recurrences_pkey PRIMARY KEY ("id"),
    CONSTRAINT cars_recurrences_period_check CHECK ("period" IN ('weekly', 'monthly', 'yearly'))
);

CREATE INDEX cars_recurrences_car_index ON cars.recurrences USING btree (car_id) WHERE active;

CREATE TABLE "cars"."fleet_log" (
    "seq" bigserial NOT NULL,
    "entry" json NOT NULL,
    "created_at" timestamp with time zone NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cars_fleet_log_pkey PRIMARY KEY ("seq")
);

CREATE OR REPLACE FUNCTION "cars"."log_fleet_change"() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
-- Appends the row change to cars.fleet_log in the same transaction, so the
-- log is exactly as durable and ordered as the change itself. The entry is
-- self-contained: readers never join back to the source tables.
DECLARE
    target record;
    entry json;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target := OLD;
    ELSE
        target := NEW;
    END IF;
    CASE TG_TABLE_NAME
        WHEN 'cars' THEN
            entry := json_build_object('t', 'car', 'op', TG_OP, 'id', target.id,
                'model', target.model, 'model_year', target.model_year,
                'lat', target.location[1], 'lng', target.location[0],
                'price_per_hour', target.price_per_hour, 'is_listed', target.is_listed);
        WHEN 'reservations' THEN
            entry := json_build_object('t', 'reservation', 'op', TG_OP, 'id', target.id,
                'car_id', target.car_id, 'start', lower(target.during), 'end', upper(target.during),
                'status', target.status, 'hold_expires_at', target.hold_expires_at);
        WHEN 'recurrences' THEN
            entry := json_build_object('t', 'recurrence', 'op', TG_OP, 'id', target.id,
                'car_id', target.car_id, 'first_start', lower(target.first_occurrence),
                'first_end', upper(target.first_occurrence), 'period', target.period,
                'timezone', target.timezone, 'active', target.active);
    END CASE;
    INSERT INTO cars.fleet_log (entry) VALUES (entry);
    RETURN NULL;
END
$$;

CREATE TRIGGER cars_fleet_log_cars AFTER INSERT OR UPDATE OR DELETE ON cars.cars FOR EACH ROW EXECUTE FUNCTION cars.log_fleet_change();
CREATE TRIGGER cars_fleet_log_reservations AFTER INSERT OR UPDATE OR DELETE ON cars.reservations FOR EACH ROW EXECUTE FUNCTION cars.log_fleet_change();
CREATE TRIGGER cars_fleet_log_recurrences AFTER INSERT OR UPDATE OR DELETE ON cars.recurrences FOR EACH ROW EXECUTE FUNCTION cars.log_fleet_change();

ALTER TABLE ONLY "cars"."identities" ADD CONSTRAINT "cars_identities_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "cars"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
ALTER TABLE ONLY "cars"."sessions" ADD CONSTRAINT "cars_sessions_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "cars"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
ALTER TABLE ONLY "cars"."cars" ADD CONSTRAINT "cars_cars_owner_id_fkey" FOREIGN KEY ("owner_id") REFERENCES "cars"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
ALTER TABLE ONLY "cars"."reservations" ADD CONSTRAINT "cars_reservations_car_id_fkey" FOREIGN KEY ("car_id") REFERENCES "cars"."cars" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
ALTER TABLE ONLY "cars"."reservations" ADD CONSTRAINT "cars_reservations_booker_id_fkey" FOREIGN KEY ("booker_id") REFERENCES "cars"."users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;
ALTER TABLE ONLY "cars"."recurrences" ADD CONSTRAINT "cars_recurrences_car_id_fkey" FOREIGN KEY ("car_id") REFERENCES "cars"."cars" ("id") ON UPDATE NO ACTION ON DELETE CASCADE;

COMMENT ON TABLE "cars"."users" IS 'Accounts, created on first sign-in. Profile fields refresh from the provider on every sign-in.';
COMMENT ON COLUMN "cars"."users"."id" IS 'Random uuid. Postgres uuids cannot carry a prefix like car-, prefixes belong in the API layer if ever wanted.';
COMMENT ON COLUMN "cars"."users"."email" IS 'Email from the provider profile at last sign-in.';
COMMENT ON COLUMN "cars"."users"."display_name" IS 'Name shown to other users, from the provider profile.';
COMMENT ON COLUMN "cars"."users"."avatar_url" IS 'Profile picture from the provider, if any.';
COMMENT ON COLUMN "cars"."users"."created_at" IS 'When the account was created.';

COMMENT ON TABLE "cars"."identities" IS 'One row per external login bound to an account. Keyed on (provider, subject) so a user can hold a Google and a GitHub identity at once, and a provider email change never forks the account.';
COMMENT ON COLUMN "cars"."identities"."provider" IS 'Login provider. Extend the CHECK when adding one.';
COMMENT ON COLUMN "cars"."identities"."subject" IS 'The provider''s stable subject id for this person, sub in OIDC terms. Never their email, emails change.';
COMMENT ON COLUMN "cars"."identities"."user_id" IS 'Account this login resolves to.';
COMMENT ON COLUMN "cars"."identities"."created_at" IS 'When this login was first linked.';

COMMENT ON TABLE "cars"."sessions" IS 'Opaque bearer sessions minted after Google OAuth. The client holds the raw token, only its SHA-256 is stored, so a database leak cannot replay sessions.';
COMMENT ON COLUMN "cars"."sessions"."id" IS 'Random uuid.';
COMMENT ON COLUMN "cars"."sessions"."user_id" IS 'Signed-in user.';
COMMENT ON COLUMN "cars"."sessions"."token_hash" IS 'Hex SHA-256 of the raw session token.';
COMMENT ON COLUMN "cars"."sessions"."expires_at" IS 'Session end. Expired rows are ignored on lookup and reaped opportunistically.';
COMMENT ON COLUMN "cars"."sessions"."created_at" IS 'When the session was minted.';

COMMENT ON TABLE "cars"."cars" IS 'Listed vehicles. An owner is just a user with rows here.';
COMMENT ON COLUMN "cars"."cars"."id" IS 'Random uuid.';
COMMENT ON COLUMN "cars"."cars"."owner_id" IS 'User who owns and listed the car.';
COMMENT ON COLUMN "cars"."cars"."model" IS 'Make and model as the owner wrote it, like Ford Mustang. Free text on purpose, a make/model taxonomy earns its keep only with search filters.';
COMMENT ON COLUMN "cars"."cars"."model_year" IS 'Model year, if the owner gave one.';
COMMENT ON COLUMN "cars"."cars"."location" IS 'Pickup point as (lng, lat) in degrees. Built-in point plus the GiST index gives indexed radius filtering and nearest-first ordering without PostGIS.';
COMMENT ON COLUMN "cars"."cars"."price_per_hour" IS 'Rental price in cents per hour, set by the owner.';
COMMENT ON COLUMN "cars"."cars"."is_listed" IS 'False hides the car from search. Existing reservations stay valid.';
COMMENT ON COLUMN "cars"."cars"."created_at" IS 'When the car was listed.';
COMMENT ON COLUMN "cars"."cars"."updated_at" IS 'Last owner edit. Written by the app, no trigger.';

COMMENT ON TABLE "cars"."reservations" IS 'One row per booked block of time, renter bookings, temporary holds, and one-off owner holds alike. The cars_reservations_no_overlap exclusion constraint is the single source of truth against double-booking: two confirmed rows for the same car can never overlap, and the database enforces it under any concurrency. Ranges are half-open, so back-to-back bookings like 1-2pm and 2-3pm do not conflict.';
COMMENT ON COLUMN "cars"."reservations"."id" IS 'Random uuid.';
COMMENT ON COLUMN "cars"."reservations"."car_id" IS 'Booked car.';
COMMENT ON COLUMN "cars"."reservations"."booker_id" IS 'User who booked, a renter or the owner.';
COMMENT ON COLUMN "cars"."reservations"."kind" IS 'rental is a paid booking, rental_hold is a short pre-payment lock that expires, owner is the owner blocking their own car.';
COMMENT ON COLUMN "cars"."reservations"."during" IS 'Booked time as a half-open range [start, end).';
COMMENT ON COLUMN "cars"."reservations"."status" IS 'Only confirmed rows block the car. Cancelled rows are kept for history and drop out of the exclusion constraint.';
COMMENT ON COLUMN "cars"."reservations"."price" IS 'Trip price in cents, frozen at booking time. Null for owner holds.';
COMMENT ON COLUMN "cars"."reservations"."hold_expires_at" IS 'Set on rental_hold rows only. A hold past this moment no longer blocks the car, and the next booking attempt deletes it.';
COMMENT ON COLUMN "cars"."reservations"."idempotency_key" IS 'Optional client-supplied key. The partial unique index on (booker_id, idempotency_key) makes booking retries return the original reservation instead of creating a second one.';
COMMENT ON COLUMN "cars"."reservations"."created_at" IS 'When the booking was made.';

COMMENT ON TABLE "cars"."recurrences" IS 'Owner-only repeating holds, like every Wednesday 9-11am. Checked at query time by cars.recurrence_overlaps, never expanded into rows. Reservations always beat recurrences: a renter booking that lands on a future occurrence stands, and the owner misses that occurrence.';
COMMENT ON COLUMN "cars"."recurrences"."id" IS 'Random uuid.';
COMMENT ON COLUMN "cars"."recurrences"."car_id" IS 'Car the schedule blocks.';
COMMENT ON COLUMN "cars"."recurrences"."first_occurrence" IS 'The first occurrence as a half-open range. Later occurrences repeat every period from it.';
COMMENT ON COLUMN "cars"."recurrences"."period" IS 'weekly, monthly, or yearly.';
COMMENT ON COLUMN "cars"."recurrences"."timezone" IS 'IANA zone name the schedule is anchored to, so occurrences keep their wall-clock time across DST.';
COMMENT ON COLUMN "cars"."recurrences"."active" IS 'False means the owner cancelled the schedule.';
COMMENT ON COLUMN "cars"."recurrences"."created_at" IS 'When the schedule was created.';

COMMENT ON TABLE "cars"."fleet_log" IS 'Ordered change feed for the in-memory search fleet. Written by triggers in the same transaction as each cars, reservations, or recurrences change, so it is exactly as durable as the data. Pods pull by seq and prune rows older than a day.';
COMMENT ON COLUMN "cars"."fleet_log"."entry" IS 'Self-contained JSON snapshot of the changed row, shaped by cars.log_fleet_change(). Readers never join back to the source tables.';
