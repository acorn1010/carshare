import { useCallback, useEffect, useState } from 'react';
import { api, type AvailableCar, type Me } from './api';
import { CarArt } from './CarArt';
import { DatePicker } from './DatePicker';
import { distance, money } from './format';
import { MapView } from './MapView';
import { Plate, useToast } from './ui';
import { BookingSheet } from './BookingSheet';

// The demo fleet lives around San Francisco.
const CITY_CENTER: readonly [number, number] = [37.77, -122.4];
const SEARCH_RANGE_METERS = 12_000;

const DURATIONS = [
  { minutes: 60, label: '1 hour' },
  { minutes: 120, label: '2 hours' },
  { minutes: 240, label: '4 hours' },
  { minutes: 480, label: '8 hours' },
  { minutes: 1440, label: '1 day' },
  { minutes: 4320, label: '3 days' },
] as const;

function defaultStart(): Date {
  const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
  tomorrow.setHours(10, 0, 0, 0);
  return tomorrow;
}

/** Search screen: when + how long on top, plate-marked map beside the result
 * list, booking sheet over it all once a car is picked. */
export function SearchPage({ me }: { readonly me: Me | null | 'loading' }) {
  const [from, setFrom] = useState<Date>(defaultStart);
  const [durationMinutes, setDurationMinutes] = useState<number>(120);
  const [cars, setCars] = useState<readonly AvailableCar[] | 'loading'>('loading');
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const { toast, show } = useToast();

  const search = useCallback(() => {
    setCars('loading');
    api
      .availability({ lat: CITY_CENTER[0], lng: CITY_CENTER[1], from, durationMinutes, rangeMeters: SEARCH_RANGE_METERS })
      .then((result) => setCars(result.cars))
      .catch((error: Error) => {
        setCars([]);
        show(error.message);
      });
  }, [from, durationMinutes, show]);

  useEffect(search, [search]);

  const selected = cars !== 'loading' ? cars.find((car) => car.id === selectedId) ?? null : null;

  return (
    <div>
      <div className="flex flex-wrap items-end gap-x-8 gap-y-4 pb-6 pt-2">
        <h1 className="text-3xl font-extrabold tracking-tight sm:text-4xl">
          Rent a car
          <br />
          by the hour.
        </h1>
        <div className="flex flex-wrap items-end gap-3">
          <DatePicker label="Pick up" value={from} onChange={setFrom} />
          <label className="block">
            <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">For</span>
            <select
              value={durationMinutes}
              onChange={(event) => setDurationMinutes(Number(event.target.value))}
              className="rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold"
            >
              {DURATIONS.map((duration) => (
                <option key={duration.minutes} value={duration.minutes}>
                  {duration.label}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-5">
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
          className="order-1 h-72 rounded-2xl shadow-card lg:order-2 lg:col-span-3 lg:h-[36rem]"
          center={CITY_CENTER}
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
