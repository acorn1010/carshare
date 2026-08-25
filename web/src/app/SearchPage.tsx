import { useCallback, useEffect, useRef, useState } from 'react';
import { api, type AvailableCar, type Me } from './api';
import { CarArt } from './CarArt';
import { DatePicker } from './DatePicker';
import { distance, money } from './format';
import { MapView } from './MapView';
import { Plate, useToast } from './ui';
import { BookingSheet } from './BookingSheet';

/** Demo fleet locations around San Francisco, standing in for a geocoder. */
const PLACES = [
  { name: 'Downtown SF', lat: 37.788, lng: -122.407 },
  { name: 'Mission', lat: 37.76, lng: -122.419 },
  { name: 'Marina', lat: 37.8, lng: -122.436 },
  { name: 'Sunset', lat: 37.753, lng: -122.494 },
  { name: 'Anywhere in SF', lat: 37.77, lng: -122.4 },
] as const;

const SEARCH_RANGE_METERS = 14_000;

function defaultStart(): Date {
  const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
  tomorrow.setHours(10, 0, 0, 0);
  return tomorrow;
}

/** Search screen, marketplace style: a hero band with the Where / From /
 * Until box up top, plate-marked map and result list below. */
export function SearchPage({ me }: { readonly me: Me | null | 'loading' }) {
  const [place, setPlace] = useState<{ name: string; lat: number; lng: number }>(PLACES[4]);
  const [from, setFrom] = useState<Date>(defaultStart);
  const [until, setUntil] = useState<Date>(() => new Date(defaultStart().getTime() + 2 * 3600 * 1000));
  const [cars, setCars] = useState<readonly AvailableCar[] | 'loading'>('loading');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { toast, show } = useToast();

  // Keep the window valid: dragging the start past the end pushes the end.
  const pickFrom = (next: Date) => {
    setFrom(next);
    if (until.getTime() <= next.getTime()) {
      setUntil(new Date(next.getTime() + 2 * 3600 * 1000));
    }
  };

  const durationMinutes = Math.max(15, Math.round((until.getTime() - from.getTime()) / 60_000));

  const search = useCallback(() => {
    setCars('loading');
    api
      .availability({ lat: place.lat, lng: place.lng, from, durationMinutes, rangeMeters: SEARCH_RANGE_METERS })
      .then((result) => setCars(result.cars))
      .catch((error: Error) => {
        setCars([]);
        show(error.message);
      });
  }, [place, from, durationMinutes, show]);

  useEffect(search, [search]);

  const selected = cars !== 'loading' ? cars.find((car) => car.id === selectedId) ?? null : null;

  return (
    <div>
      <section className="rounded-3xl bg-pine-800 px-6 py-10 sm:px-10 sm:py-14">
        <h1 className="max-w-xl text-3xl font-extrabold tracking-tight text-paper-50 sm:text-5xl">
          Rent a car by the hour, from people nearby.
        </h1>
        <p className="pt-2 text-sm font-medium text-pine-200">
          Every price is locked when you book, and a booked hour can never be sold twice.
        </p>
        <div className="mt-6 flex flex-wrap items-end gap-x-5 gap-y-4 rounded-2xl bg-paper-50 p-4 shadow-sheet">
          <WherePicker place={place} onChange={setPlace} />
          <DatePicker label="From" value={from} onChange={pickFrom} />
          <DatePicker label="Until" value={until} onChange={setUntil} />
          <button
            type="button"
            onClick={search}
            aria-label="Search"
            className="ml-auto rounded-xl bg-pine-600 px-5 py-2.5 text-base font-extrabold text-paper-50 transition-colors duration-75 hover:bg-pine-700"
          >
            Find cars
          </button>
        </div>
      </section>

      <div className="grid gap-4 pt-6 lg:grid-cols-5">
        <div className="order-2 lg:order-1 lg:col-span-2">
          {cars === 'loading' ? (
            <p className="py-8 text-sm text-paper-600">Looking for free cars…</p>
          ) : cars.length === 0 ? (
            <p className="py-8 text-sm text-paper-600">No cars free for that window. Try another time.</p>
          ) : (
            <ul className="flex flex-col gap-2">
              {cars.map((car, index) => (
                <li key={car.id}>
                  <button
                    type="button"
                    onClick={() => setSelectedId(car.id)}
                    className={`animate-rise flex w-full items-center gap-4 rounded-xl border bg-paper-50 px-4 py-3 text-left shadow-card transition-colors duration-75 motion-reduce:animate-none ${
                      selectedId === car.id ? 'border-pine-600' : 'border-transparent hover:border-paper-400'
                    }`}
                    style={{ animationDelay: `${Math.min(index, 12) * 25}ms` }}
                  >
                    <CarArt carId={car.id} className="w-16 shrink-0" />
                    <span className="flex min-w-0 flex-col">
                      <span className="truncate text-sm font-bold">
                        {car.model || 'Car'}
                        {car.model_year ? <span className="font-medium text-paper-600"> · {car.model_year}</span> : null}
                      </span>
                      <span className="text-xs text-paper-600">
                        {money(car.price_per_hour)}/h · {distance(car.distance_meters)} away
                      </span>
                    </span>
                    <Plate tone={selectedId === car.id ? 'pine' : 'ink'} className="ml-auto text-lg">
                      {money(car.trip_price)}
                    </Plate>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <MapView
          key={place.name}
          className="order-1 h-72 rounded-2xl shadow-card lg:order-2 lg:col-span-3 lg:h-[34rem]"
          center={[place.lat, place.lng]}
          cars={cars === 'loading' ? [] : cars.map((car) => ({
            id: car.id,
            lat: car.lat,
            lng: car.lng,
            priceCents: car.trip_price,
            selected: car.id === selectedId,
          }))}
          onSelect={setSelectedId}
        />
      </div>

      {selected ? (
        <BookingSheet
          key={selected.id}
          car={selected}
          from={from}
          durationMinutes={durationMinutes}
          me={me}
          onClose={() => setSelectedId(null)}
          onBooked={search}
          onConflict={() => {
            show('Someone just took this slot', 'clay');
            search();
          }}
        />
      ) : null}
      {toast}
    </div>
  );
}

/** Where field: preset neighborhoods plus the browser's own location, a
 * stand-in for a geocoder that keeps the demo self-contained. */
function WherePicker({ place, onChange }: {
  readonly place: { name: string; lat: number; lng: number };
  readonly onChange: (place: { name: string; lat: number; lng: number }) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onDown = (event: MouseEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  const useMyLocation = () => {
    navigator.geolocation.getCurrentPosition(
      (position) => onChange({ name: 'Near me', lat: position.coords.latitude, lng: position.coords.longitude }),
      () => onChange(PLACES[4]),
    );
    setOpen(false);
  };

  return (
    <div ref={root} className="relative">
      <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">Where</span>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold hover:border-paper-500"
      >
        {place.name}
      </button>
      {open ? (
        <ul className="animate-rise absolute z-1050 mt-2 w-48 overflow-hidden rounded-xl border border-paper-300 bg-paper-50 shadow-sheet motion-reduce:animate-none">
          <li>
            <button type="button" onClick={useMyLocation} className="w-full px-3 py-2 text-left text-sm font-semibold hover:bg-paper-200">
              ⌖ Current location
            </button>
          </li>
          {PLACES.map((option) => (
            <li key={option.name}>
              <button
                type="button"
                onClick={() => {
                  onChange(option);
                  setOpen(false);
                }}
                className={`w-full px-3 py-2 text-left text-sm font-semibold hover:bg-paper-200 ${
                  option.name === place.name ? 'text-pine-700' : ''
                }`}
              >
                {option.name}
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}
