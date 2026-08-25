import { useCallback, useEffect, useRef, useState, type UIEvent } from 'react';
import { api, type AvailableCar, type Me } from './api';
import { CarPhoto } from './CarArt';
import { RangePicker } from './RangePicker';
import { distance, money } from './format';
import { MapView } from './MapView';
import { Plate, useToast } from './ui';
import { BookingSheet } from './BookingSheet';

/** The fleet spans the US, Japan, and Europe. These presets stand in for a
 * geocoder, and Current location works anywhere seeded. */
const PLACES = [
  { name: 'San Francisco', lat: 37.77, lng: -122.42 },
  { name: 'New York', lat: 40.73, lng: -73.99 },
  { name: 'London', lat: 51.51, lng: -0.12 },
  { name: 'Paris', lat: 48.86, lng: 2.35 },
  { name: 'Berlin', lat: 52.52, lng: 13.4 },
  { name: 'Tokyo', lat: 35.68, lng: 139.75 },
] as const;

const SEARCH_RANGE_METERS = 14_000;

function defaultStart(): Date {
  const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
  tomorrow.setHours(10, 0, 0, 0);
  return tomorrow;
}

/** Search screen, marketplace style: a hero band with the Where / From /
 * Until box up top, then a viewport-locked split with the car list scrolling
 * on the left and the map always on screen to the right. Scrolling the list
 * collapses the hero down to just the search box. */
export function SearchPage({ me }: { readonly me: Me | null | 'loading' }) {
  const [place, setPlace] = useState<{ name: string; lat: number; lng: number }>(PLACES[0]);
  const [from, setFrom] = useState<Date>(defaultStart);
  const [until, setUntil] = useState<Date>(() => new Date(defaultStart().getTime() + 2 * 3600 * 1000));
  const [sort, setSort] = useState<'price' | 'distance'>('distance');
  const [movedCenter, setMovedCenter] = useState<{ lat: number; lng: number } | null>(null);
  const [cars, setCars] = useState<readonly AvailableCar[] | 'loading'>('loading');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [heroCollapsed, setHeroCollapsed] = useState(false);
  const listPane = useRef<HTMLDivElement>(null);
  const { toast, show } = useToast();

  const durationMinutes = Math.max(15, Math.round((until.getTime() - from.getTime()) / 60_000));

  const search = useCallback(() => {
    setCars('loading');
    api
      .availability({ lat: place.lat, lng: place.lng, from, durationMinutes, rangeMeters: SEARCH_RANGE_METERS, sort })
      .then((result) => setCars(result.cars))
      .catch((error: Error) => {
        setCars([]);
        show(error.message);
      });
  }, [place, from, durationMinutes, sort, show]);

  useEffect(search, [search]);

  const selected = cars !== 'loading' ? cars.find((car) => car.id === selectedId) ?? null : null;

  const onChangePlaceFromMap = (at: { lat: number; lng: number }) => {
    setPlace({ name: 'This area', lat: at.lat, lng: at.lng });
  };

  // The hero collapses once the list has scrolled and comes back at the top.
  const onListScroll = (event: UIEvent<HTMLDivElement>) => {
    setHeroCollapsed(event.currentTarget.scrollTop > 0);
  };

  // The page itself never scrolls, so a wheel anywhere outside the map acts
  // on the car list: the hero, the header, the gutters beside the content.
  // The list pane scrolls natively, the map wheel zooms, and fixed overlays
  // (the booking sheet) keep their wheel to themselves.
  useEffect(() => {
    const onWheel = (event: globalThis.WheelEvent) => {
      const pane = listPane.current;
      const target = event.target instanceof Element ? event.target : null;
      if (!pane || !target || pane.contains(target)) {
        return;
      }
      if (target.closest('.leaflet-container') || target.closest('.fixed')) {
        return;
      }
      pane.scrollBy({ top: event.deltaY });
    };
    window.addEventListener('wheel', onWheel, { passive: true });
    return () => window.removeEventListener('wheel', onWheel);
  }, []);

  return (
    <div className="flex h-full flex-col">
      <section className={`rounded-3xl bg-pine-800 ${heroCollapsed ? 'p-3 sm:p-4' : 'px-6 py-10 sm:px-10 sm:py-14'}`}>
        {heroCollapsed ? null : (
          <>
            <h1 className="text-center text-3xl font-extrabold tracking-tight text-paper-50 sm:text-5xl">
              Rent a car, fast
            </h1>
            <p className="pt-2 text-center text-sm font-medium text-pine-200">
              Every price is locked when you book, and a booked hour can never be sold twice.
            </p>
          </>
        )}
        <div className={`flex flex-wrap items-end gap-x-5 gap-y-4 rounded-2xl bg-paper-50 p-4 shadow-sheet ${heroCollapsed ? '' : 'mt-6'}`}>
          <WherePicker place={place} onChange={setPlace} />
          <RangePicker
            from={from}
            until={until}
            onChange={(nextFrom, nextUntil) => {
              setFrom(nextFrom);
              setUntil(nextUntil);
            }}
          />
          <button
            type="button"
            onClick={search}
            aria-label="Search"
            className="ml-auto rounded-xl bg-pine-600 px-5 py-2.5 text-base font-extrabold text-paper-50 transition-colors duration-75 hover:bg-pine-700 active:bg-pine-800"
          >
            Find cars
          </button>
        </div>
      </section>

      <div className="grid min-h-0 flex-1 grid-rows-[18rem_minmax(0,1fr)] gap-4 pt-4 lg:grid-cols-5 lg:grid-rows-[minmax(0,1fr)]">
        <div className="order-2 flex min-h-0 flex-col lg:order-1 lg:col-span-2">
          <div className="flex items-center gap-1 pb-3">
            <span className="pr-2 text-xs font-semibold uppercase tracking-wide text-paper-600">Sort</span>
            {(['distance', 'price'] as const).map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setSort(option)}
                className={`rounded-full px-3 py-1 text-xs font-bold transition-colors duration-75 ${
                  sort === option ? 'bg-paper-900 text-paper-50' : 'bg-paper-200 text-paper-700 hover:bg-paper-300 active:bg-paper-400'
                }`}
              >
                {option === 'price' ? 'Cheapest' : 'Closest'}
              </button>
            ))}
          </div>
          <div ref={listPane} onScroll={onListScroll} className="-mx-1 px-1 pb-1 lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
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
                      className={`animate-rise flex w-full items-center gap-4 overflow-hidden rounded-xl border bg-paper-50 pr-4 text-left shadow-card transition-colors duration-75 motion-reduce:animate-none ${
                        selectedId === car.id ? 'border-pine-600 active:bg-paper-100' : 'border-transparent hover:border-paper-400 active:bg-paper-100'
                      }`}
                      style={{ animationDelay: `${Math.min(index, 12) * 25}ms` }}
                    >
                      <CarPhoto model={car.model} carId={car.id} className="h-20 w-28 shrink-0 self-stretch" />
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
            <p className="mt-4 border-t border-paper-300 py-4 text-xs text-paper-600">
              A demo of an open-source reservation engine. Double-booking is impossible by construction, try it: book
              the same car in two tabs.
            </p>
          </div>
        </div>
        <div className="relative order-1 min-h-0 lg:order-2 lg:col-span-3">
          <MapView
            className="h-full w-full rounded-2xl shadow-card"
            center={[place.lat, place.lng]}
            cars={cars === 'loading' ? [] : cars.map((car) => ({
              id: car.id,
              lat: car.lat,
              lng: car.lng,
              priceCents: car.trip_price,
              selected: car.id === selectedId,
            }))}
            onSelect={setSelectedId}
            onMoved={(lat, lng) => setMovedCenter({ lat, lng })}
          />
          {movedCenter ? (
            <div className="pointer-events-none absolute inset-x-0 top-3 z-500 flex justify-center">
              <button
                type="button"
                onClick={() => {
                  onChangePlaceFromMap(movedCenter);
                  setMovedCenter(null);
                }}
                className="animate-rise pointer-events-auto rounded-full bg-paper-900 px-4 py-2 text-sm font-bold text-paper-50 shadow-sheet transition-colors duration-75 hover:bg-paper-800 active:bg-paper-700 motion-reduce:animate-none"
              >
                ↻ Search this area
              </button>
            </div>
          ) : null}
        </div>
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
      () => onChange(PLACES[0]),
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
            <button type="button" onClick={useMyLocation} className="w-full px-3 py-2 text-left text-sm font-semibold hover:bg-paper-200 active:bg-paper-300">
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
                className={`w-full px-3 py-2 text-left text-sm font-semibold hover:bg-paper-200 active:bg-paper-300 ${
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
