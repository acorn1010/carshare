import { useEffect, useState } from 'react';
import { ApiError, api, signIn, type AvailableCar, type Me, type Reservation } from './api';
import { countdown, money, window as formatWindow } from './format';
import { Button, Plate, Stamp } from './ui';

type Phase =
  | { readonly step: 'quote' }
  | { readonly step: 'holding' }
  | { readonly step: 'held'; readonly hold: Reservation }
  | { readonly step: 'booked' }
  | { readonly step: 'taken' }
  | { readonly step: 'price_changed' };

/** The booking flow as a bottom sheet: quote, hold with a live countdown,
 * confirm. A lost race gets the JUST TAKEN stamp, which is the backend's
 * exclusion constraint made visible. */
export function BookingSheet({ car, from, durationMinutes, me, onClose, onBooked, onConflict }: {
  readonly car: AvailableCar;
  readonly from: Date;
  readonly durationMinutes: number;
  readonly me: Me | null | 'loading';
  readonly onClose: () => void;
  readonly onBooked: () => void;
  readonly onConflict: () => void;
}) {
  const [phase, setPhase] = useState<Phase>({ step: 'quote' });
  const end = new Date(from.getTime() + durationMinutes * 60_000);

  const fail = (error: unknown) => {
    if (error instanceof ApiError && (error.code === 'conflict' || error.code === 'owner_schedule_conflict')) {
      setPhase({ step: 'taken' });
      onConflict();
      return;
    }
    if (error instanceof ApiError && error.code === 'price_changed') {
      setPhase({ step: 'price_changed' });
      return;
    }
    setPhase({ step: 'quote' });
    throw error;
  };

  const hold = () => {
    setPhase({ step: 'holding' });
    api
      .order({
        car_id: car.id,
        price: car.trip_price,
        from: from.toISOString(),
        duration_minutes: durationMinutes,
        kind: 'rental_hold',
        idempotency_key: crypto.randomUUID(),
      })
      .then((reservation) => setPhase({ step: 'held', hold: reservation }))
      .catch(fail);
  };

  const confirm = (holdId: string) => {
    api
      .confirmHold(holdId)
      .then(() => {
        setPhase({ step: 'booked' });
        onBooked();
      })
      .catch(fail);
  };

  const release = (holdId: string) => {
    api.cancel(holdId).finally(onClose);
  };

  return (
    <div className="fixed inset-x-0 bottom-0 z-1000 flex justify-center px-4 pb-4">
      <div className="animate-rise w-full max-w-xl rounded-2xl border border-paper-300 bg-paper-50 p-5 shadow-sheet motion-reduce:animate-none">
        <div className="flex items-start gap-4">
          <Plate tone="ink" className="text-2xl">
            {money(car.trip_price)}
          </Plate>
          <div className="min-w-0">
            <p className="text-sm font-bold">{formatWindow(from.toISOString(), end.toISOString())}</p>
            <p className="text-xs text-paper-600">
              {money(car.price_per_hour)} per hour · price locked when you book
            </p>
          </div>
          <button type="button" onClick={onClose} aria-label="Close" className="ml-auto -mr-1 -mt-1 rounded-md px-2 py-1 text-paper-500 hover:bg-paper-200">
            ✕
          </button>
        </div>

        <div className="mt-4">
          {phase.step === 'quote' || phase.step === 'holding' ? (
            me === 'loading' ? null : me ? (
              <Button onClick={hold} disabled={phase.step === 'holding'} className="w-full">
                {phase.step === 'holding' ? 'Holding…' : 'Hold this car'}
              </Button>
            ) : (
              <Button onClick={signIn} className="w-full">
                Sign in with Google to book
              </Button>
            )
          ) : null}

          {phase.step === 'held' ? <HeldControls hold={phase.hold} onConfirm={confirm} onRelease={release} onExpired={onClose} /> : null}

          {phase.step === 'booked' ? (
            <div className="flex items-center justify-between gap-4">
              <Stamp tone="pine">Booked</Stamp>
              <a href="/trips" className="text-sm font-semibold text-pine-700 hover:underline">
                See it in my trips →
              </a>
            </div>
          ) : null}

          {phase.step === 'taken' ? (
            <div className="flex items-center justify-between gap-4">
              <Stamp tone="clay">Just taken</Stamp>
              <p className="text-sm text-paper-600">Someone else booked this window first. Pick another car.</p>
            </div>
          ) : null}

          {phase.step === 'price_changed' ? (
            <div className="flex items-center justify-between gap-4">
              <Stamp tone="clay">Repriced</Stamp>
              <p className="text-sm text-paper-600">The owner changed the price. Search again for a fresh quote.</p>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function HeldControls({ hold, onConfirm, onRelease, onExpired }: {
  readonly hold: Reservation;
  readonly onConfirm: (id: string) => void;
  readonly onRelease: (id: string) => void;
  readonly onExpired: () => void;
}) {
  const [secondsLeft, setSecondsLeft] = useState(() => remaining(hold));

  useEffect(() => {
    const timer = setInterval(() => {
      const seconds = remaining(hold);
      setSecondsLeft(seconds);
      if (seconds <= 0) {
        onExpired();
      }
    }, 1000);
    return () => clearInterval(timer);
  }, [hold, onExpired]);

  return (
    <div className="flex items-center gap-3">
      <Button onClick={() => onConfirm(hold.id)} className="flex-1">
        Confirm booking
      </Button>
      <Plate tone="marigold" className="text-base">
        held {countdown(secondsLeft)}
      </Plate>
      <Button tone="ghost" onClick={() => onRelease(hold.id)}>
        Release
      </Button>
    </div>
  );
}

function remaining(hold: Reservation): number {
  return hold.hold_expires_at ? Math.floor((new Date(hold.hold_expires_at).getTime() - Date.now()) / 1000) : 0;
}
