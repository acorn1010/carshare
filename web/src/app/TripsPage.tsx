import { useCallback, useEffect, useState } from 'react';
import { ApiError, api, signIn, type Me, type Reservation } from './api';
import { countdown, money, window as formatWindow } from './format';
import { Button, Chip, Plate, useToast } from './ui';

/** My trips: upcoming first, holds carry their countdown and a confirm
 * shortcut, cancellation surfaces the 24 hour rule as a plain sentence. */
export function TripsPage({ me }: { readonly me: Me | null | 'loading' }) {
  const [reservations, setReservations] = useState<readonly Reservation[] | 'loading'>('loading');
  const { toast, show } = useToast();

  const reload = useCallback(() => {
    api.myReservations().then((result) => setReservations(result.reservations), () => setReservations([]));
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
        <h1 className="text-3xl font-extrabold tracking-tight">Your trips live here.</h1>
        <Button onClick={signIn}>Sign in with Google</Button>
      </div>
    );
  }

  const act = (work: Promise<unknown>, done: string) => {
    work
      .then(() => {
        show(done, 'pine');
        reload();
      })
      .catch((error: unknown) => show(error instanceof ApiError ? error.message : 'something went wrong'));
  };

  return (
    <div className="py-2">
      <h1 className="pb-6 text-3xl font-extrabold tracking-tight">My trips</h1>
      {reservations === 'loading' ? (
        <p className="text-sm text-paper-600">Loading…</p>
      ) : reservations.length === 0 ? (
        <p className="text-sm text-paper-600">
          Nothing yet.{' '}
          <a href="/" className="font-semibold text-pine-700 hover:underline">
            Find a car →
          </a>
        </p>
      ) : (
        <ul className="flex max-w-2xl flex-col gap-2">
          {reservations.map((reservation) => (
            <TripRow key={reservation.id} reservation={reservation} onAct={act} />
          ))}
        </ul>
      )}
      {toast}
    </div>
  );
}

function TripRow({ reservation, onAct }: {
  readonly reservation: Reservation;
  readonly onAct: (work: Promise<unknown>, done: string) => void;
}) {
  const isPast = new Date(reservation.end).getTime() < Date.now();
  const isLiveHold =
    reservation.kind === 'rental_hold' &&
    reservation.status === 'confirmed' &&
    !!reservation.hold_expires_at &&
    new Date(reservation.hold_expires_at).getTime() > Date.now();
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    if (!isLiveHold) {
      return;
    }
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [isLiveHold]);

  return (
    <li className="flex flex-wrap items-center gap-3 rounded-xl border border-paper-200 bg-paper-50 px-4 py-3 shadow-card">
      {reservation.price !== undefined ? <Plate className="text-base">{money(reservation.price)}</Plate> : <Plate className="text-base">own</Plate>}
      <div className="min-w-0">
        <p className="text-sm font-bold">{formatWindow(reservation.start, reservation.end)}</p>
        <p className="text-xs text-paper-600">car {reservation.car_id.slice(0, 8)}</p>
      </div>
      <div className="ml-auto flex items-center gap-2">
        {reservation.status === 'cancelled' ? (
          <Chip tone="paper">cancelled</Chip>
        ) : isLiveHold && reservation.hold_expires_at ? (
          <>
            <Plate tone="marigold" className="text-sm">
              held {countdown(Math.floor((new Date(reservation.hold_expires_at).getTime() - now) / 1000))}
            </Plate>
            <Button onClick={() => onAct(api.confirmHold(reservation.id), 'Booked')}>Confirm</Button>
          </>
        ) : isPast ? (
          <Chip tone="paper">done</Chip>
        ) : (
          <Chip tone={reservation.kind === 'owner' ? 'marigold' : 'pine'}>
            {reservation.kind === 'owner' ? 'own hold' : 'booked'}
          </Chip>
        )}
        {reservation.status === 'confirmed' && !isPast ? (
          <Button tone="clay" onClick={() => onAct(api.cancel(reservation.id), 'Cancelled')}>
            Cancel
          </Button>
        ) : null}
      </div>
    </li>
  );
}
