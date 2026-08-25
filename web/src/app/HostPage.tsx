import { useCallback, useEffect, useState } from 'react';
import { ApiError, api, signIn, type Calendar, type Car, type Me } from './api';
import { CarPhoto } from './CarArt';
import { DatePicker } from './DatePicker';
import { money, window as formatWindow } from './format';
import { MapView } from './MapView';
import { Button, Chip, Plate, useToast } from './ui';

const CITY_CENTER: readonly [number, number] = [37.77, -122.4];

// Mirror the server's bounds so the form rejects the same values the API does.
const MIN_MODEL_YEAR = 1900;
const MAX_MODEL_YEAR = new Date().getFullYear() + 1;
const MAX_DOLLARS_PER_HOUR = 500;

function isValidRate(dollars: string): boolean {
  return Number(dollars) > 0 && Number(dollars) <= MAX_DOLLARS_PER_HOUR;
}

// Snaps an out-of-range value into range when the input loses focus, so the
// user sees the correction instead of a disabled button with no explanation.
function clamp(value: string, min: number, max: number): string {
  if (value === '') {
    return '';
  }
  return String(Math.min(max, Math.max(min, Number(value))));
}

/** Host screen: list a car by dropping a pin, edit price, hide from search,
 * and manage the calendar with repeating holds. */
export function HostPage({ me }: { readonly me: Me | null | 'loading' }) {
  const [cars, setCars] = useState<readonly Car[] | 'loading'>('loading');
  const { toast, show } = useToast();

  const reload = useCallback(() => {
    api.myCars().then((result) => setCars(result.cars), () => setCars([]));
  }, []);

  useEffect(() => {
    if (me && me !== 'loading') {
      reload();
    }
  }, [me, reload]);

  if (me === 'loading') {
    return null;
  }
  if (!me) {
    return (
      <div className="flex flex-col items-start gap-4 py-12">
        <h1 className="text-3xl font-extrabold tracking-tight">Put your car to work.</h1>
        <Button onClick={signIn}>Sign in with Google</Button>
      </div>
    );
  }

  const fail = (error: unknown) => show(error instanceof ApiError ? error.message : 'something went wrong');

  return (
    <div className="py-2">
      <h1 className="pb-6 text-3xl font-extrabold tracking-tight">Host</h1>
      <div className="grid items-start gap-6 lg:grid-cols-2">
        <AddCar
          onAdd={(lat, lng, priceCents, model, modelYear) =>
            api
              .createCar({ lat, lng, price_per_hour: priceCents, model, model_year: modelYear })
              .then(() => {
                show('Car listed', 'pine');
                reload();
              })
              .catch(fail)
          }
        />
        <div className="flex flex-col gap-4">
          {cars === 'loading' ? (
            <p className="text-sm text-paper-600">Loading…</p>
          ) : cars.length === 0 ? (
            <p className="text-sm text-paper-600">No cars listed yet. Drop a pin to add your first.</p>
          ) : (
            cars.map((car) => <CarCard key={car.id} car={car} onChange={reload} onFail={fail} onDone={(text) => show(text, 'pine')} />)
          )}
        </div>
      </div>
      {toast}
    </div>
  );
}

function AddCar({ onAdd }: {
  readonly onAdd: (lat: number, lng: number, priceCents: number, model: string, modelYear?: number) => void;
}) {
  const [pin, setPin] = useState<readonly [number, number] | null>(null);
  const [dollarsPerHour, setDollarsPerHour] = useState('12');
  const [model, setModel] = useState('');
  const [year, setYear] = useState('');

  // Year is optional, but a filled-in year must be plausible.
  const isValidYear = year === ''
    || (Number(year) >= MIN_MODEL_YEAR && Number(year) <= MAX_MODEL_YEAR);

  return (
    <section className="rounded-2xl border border-paper-200 bg-paper-50 p-4 shadow-card">
      <h2 className="pb-1 text-lg font-extrabold">List a car</h2>
      <p className="pb-3 text-xs text-paper-600">Tap the map where the car parks.</p>
      <MapView className="h-64 rounded-xl" center={CITY_CENTER} cars={[]} pin={pin} onPick={(lat, lng) => setPin([lat, lng])} />
      <div className="flex flex-wrap items-end gap-3 pt-4">
        <label className="block grow">
          <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">Make and model</span>
          <input
            type="text"
            placeholder="Ford Mustang"
            value={model}
            onChange={(event) => setModel(event.target.value)}
            className="w-full rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">Year</span>
          <input
            type="number"
            placeholder="2021"
            min={MIN_MODEL_YEAR}
            max={MAX_MODEL_YEAR}
            value={year}
            onChange={(event) => setYear(event.target.value)}
            onBlur={() => setYear(clamp(year, MIN_MODEL_YEAR, MAX_MODEL_YEAR))}
            className="w-20 rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold tabular-nums"
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">$ per hour</span>
          <input
            type="number"
            min="1"
            max={MAX_DOLLARS_PER_HOUR}
            step="1"
            value={dollarsPerHour}
            onChange={(event) => setDollarsPerHour(event.target.value)}
            onBlur={() => setDollarsPerHour(clamp(dollarsPerHour, 1, MAX_DOLLARS_PER_HOUR))}
            className="w-24 rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold tabular-nums"
          />
        </label>
        <Button
          disabled={!pin || !isValidRate(dollarsPerHour) || model.trim() === '' || !isValidYear}
          onClick={() =>
            pin && onAdd(pin[0], pin[1], Math.round(Number(dollarsPerHour) * 100), model.trim(),
              year === '' ? undefined : Number(year))
          }
        >
          List it
        </Button>
      </div>
    </section>
  );
}

function CarCard({ car, onChange, onFail, onDone }: {
  readonly car: Car;
  readonly onChange: () => void;
  readonly onFail: (error: unknown) => void;
  readonly onDone: (text: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [calendar, setCalendar] = useState<Calendar | null>(null);
  const [dollars, setDollars] = useState(() => String(car.price_per_hour / 100));

  const loadCalendar = useCallback(() => {
    api.calendar(car.id, new Date(), new Date(Date.now() + 30 * 24 * 3600 * 1000)).then(setCalendar, onFail);
  }, [car.id, onFail]);

  useEffect(() => {
    if (open) {
      loadCalendar();
    }
  }, [open, loadCalendar]);

  return (
    <section className="rounded-2xl border border-paper-200 bg-paper-50 p-4 shadow-card">
      <div className="flex flex-wrap items-center gap-3">
        <CarPhoto model={car.model} carId={car.id} className="h-12 w-18 shrink-0 rounded-lg" />
        <span className="flex flex-col">
          <span className="text-sm font-bold">
            {car.model || 'Car'}
            {car.model_year ? <span className="font-medium text-paper-600"> · {car.model_year}</span> : null}
          </span>
          <Plate className="mt-0.5 self-start text-sm">{money(car.price_per_hour)}/h</Plate>
        </span>
        {car.is_listed ? <Chip tone="pine">listed</Chip> : <Chip tone="paper">hidden</Chip>}
        <div className="ml-auto flex items-center gap-2">
          <Button
            tone="ghost"
            onClick={() =>
              api
                .updateCar(car.id, { is_listed: !car.is_listed })
                .then(() => {
                  onDone(car.is_listed ? 'Hidden from search' : 'Back in search');
                  onChange();
                })
                .catch(onFail)
            }
          >
            {car.is_listed ? 'Hide' : 'Show'}
          </Button>
          <Button tone="ghost" onClick={() => setOpen(!open)}>
            {open ? 'Close' : 'Calendar'}
          </Button>
        </div>
      </div>

      {open ? (
        <div className="flex flex-col gap-4 pt-4">
          <div className="flex items-end gap-2">
            <label className="block">
              <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">$ per hour</span>
              <input
                type="number"
                min="1"
                max={MAX_DOLLARS_PER_HOUR}
                step="1"
                value={dollars}
                onChange={(event) => setDollars(event.target.value)}
                onBlur={() => setDollars(clamp(dollars, 1, MAX_DOLLARS_PER_HOUR))}
                className="w-24 rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold tabular-nums"
              />
            </label>
            <Button
              tone="ghost"
              disabled={!isValidRate(dollars)}
              onClick={() =>
                api
                  .updateCar(car.id, { price_per_hour: Math.round(Number(dollars) * 100) })
                  .then(() => {
                    onDone('Price updated. Existing bookings keep their old price.');
                    onChange();
                  })
                  .catch(onFail)
              }
            >
              Save price
            </Button>
          </div>

          <div>
            <h3 className="pb-2 text-sm font-extrabold">Next 30 days</h3>
            {calendar === null ? (
              <p className="text-xs text-paper-600">Loading…</p>
            ) : calendar.reservations.length === 0 ? (
              <p className="text-xs text-paper-600">No bookings yet.</p>
            ) : (
              <ul className="flex flex-col gap-1">
                {calendar.reservations.map((reservation) => (
                  <li key={reservation.id} className="flex items-center gap-2 text-sm tabular-nums">
                    <Chip tone={reservation.kind === 'owner' ? 'marigold' : 'pine'}>
                      {reservation.kind === 'owner' ? 'you' : 'rented'}
                    </Chip>
                    {formatWindow(reservation.start, reservation.end)}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div>
            <h3 className="pb-2 text-sm font-extrabold">Repeating holds</h3>
            {calendar !== null && calendar.schedules.length > 0 ? (
              <ul className="flex flex-col gap-1 pb-2">
                {calendar.schedules.map((schedule) => (
                  <li key={schedule.id} className="flex items-center gap-2 text-sm">
                    <Chip tone="marigold">{schedule.period}</Chip>
                    <span className="tabular-nums">{formatWindow(schedule.first_start, schedule.first_end)}</span>
                    <Button tone="clay" onClick={() => api.deleteSchedule(schedule.id).then(loadCalendar).catch(onFail)}>
                      Remove
                    </Button>
                  </li>
                ))}
              </ul>
            ) : null}
            <AddSchedule
              onAdd={(from, durationMinutes, period) =>
                api
                  .createSchedule({
                    car_id: car.id,
                    from,
                    duration_minutes: durationMinutes,
                    period,
                    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
                  })
                  .then(() => {
                    onDone('Repeating hold added. Existing bookings on it stand.');
                    loadCalendar();
                  })
                  .catch(onFail)
              }
            />
          </div>
        </div>
      ) : null}
    </section>
  );
}

function AddSchedule({ onAdd }: {
  readonly onAdd: (fromIso: string, durationMinutes: number, period: 'weekly' | 'monthly' | 'yearly') => void;
}) {
  const [from, setFrom] = useState<Date>(() => new Date(Date.now() + 24 * 3600 * 1000));
  const [durationMinutes, setDurationMinutes] = useState(120);
  const [period, setPeriod] = useState<'weekly' | 'monthly' | 'yearly'>('weekly');

  return (
    <div className="flex flex-wrap items-end gap-2">
      <DatePicker label="First occurrence" value={from} onChange={setFrom} />
      <select
        value={durationMinutes}
        onChange={(event) => setDurationMinutes(Number(event.target.value))}
        className="rounded-lg border border-paper-300 bg-paper-50 px-2 py-2 text-sm font-semibold"
        aria-label="Duration"
      >
        <option value={60}>1 hour</option>
        <option value={120}>2 hours</option>
        <option value={240}>4 hours</option>
        <option value={480}>8 hours</option>
      </select>
      <select
        value={period}
        onChange={(event) => setPeriod(event.target.value as 'weekly' | 'monthly' | 'yearly')}
        className="rounded-lg border border-paper-300 bg-paper-50 px-2 py-2 text-sm font-semibold"
        aria-label="Repeats"
      >
        <option value="weekly">weekly</option>
        <option value="monthly">monthly</option>
        <option value="yearly">yearly</option>
      </select>
      <Button tone="ghost" onClick={() => onAdd(from.toISOString(), durationMinutes, period)}>
        Add
      </Button>
    </div>
  );
}
