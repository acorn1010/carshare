/** Typed client for the carshare API. Same-origin in production (the Worker
 * proxies /v1), proxied by Vite in dev, so there is never any CORS. */

export type Me = {
  readonly id: string;
  readonly email: string;
  readonly display_name: string;
  readonly avatar_url: string;
};

export type Car = {
  readonly id: string;
  readonly owner_id: string;
  readonly model: string;
  readonly model_year?: number;
  readonly lat: number;
  readonly lng: number;
  readonly price_per_hour: number;
  readonly is_listed: boolean;
};

export type AvailableCar = Car & {
  readonly trip_price: number;
  readonly distance_meters: number;
};

export type Reservation = {
  readonly id: string;
  readonly car_id: string;
  readonly kind: 'rental' | 'rental_hold' | 'owner';
  readonly status: 'confirmed' | 'cancelled';
  readonly start: string;
  readonly end: string;
  readonly price?: number;
  readonly hold_expires_at?: string;
};

export type Schedule = {
  readonly id: string;
  readonly first_start: string;
  readonly first_end: string;
  readonly period: 'weekly' | 'monthly' | 'yearly';
  readonly timezone: string;
};

export type Calendar = {
  readonly reservations: readonly Reservation[];
  readonly schedules: readonly Schedule[];
};

/** Error with the API's stable code, so screens can branch on `conflict`
 * versus `price_changed` instead of parsing prose. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  });
  if (response.status === 204) {
    return undefined as T;
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = (body as { error?: { code: string; message: string } }).error;
    throw new ApiError(response.status, error?.code ?? 'unknown', error?.message ?? 'something went wrong');
  }
  return body as T;
}

export const api = {
  me: () => request<Me>('/v1/me'),
  logout: () => request<void>('/v1/auth/logout', { method: 'POST' }),

  availability: (params: { lat: number; lng: number; from: Date; durationMinutes: number; rangeMeters: number }) =>
    request<{ cars: AvailableCar[] }>(
      `/v1/availability?lat=${params.lat}&lng=${params.lng}&from=${encodeURIComponent(params.from.toISOString())}` +
        `&duration_minutes=${params.durationMinutes}&range_meters=${params.rangeMeters}`,
    ),

  order: (body: {
    car_id: string;
    price: number;
    from: string;
    duration_minutes: number;
    kind: 'rental' | 'rental_hold' | 'owner';
    idempotency_key?: string;
  }) => request<Reservation>('/v1/reservations', { method: 'POST', body: JSON.stringify(body) }),

  confirmHold: (id: string) => request<Reservation>(`/v1/reservations/${id}/confirm`, { method: 'POST' }),
  cancel: (id: string) => request<void>(`/v1/reservations/${id}`, { method: 'DELETE' }),
  myReservations: () => request<{ reservations: Reservation[] }>('/v1/me/reservations'),

  myCars: () => request<{ cars: Car[] }>('/v1/me/cars'),
  createCar: (body: { lat: number; lng: number; price_per_hour: number; model: string; model_year?: number }) =>
    request<Car>('/v1/cars', { method: 'POST', body: JSON.stringify(body) }),
  updateCar: (id: string, body: Partial<{ lat: number; lng: number; price_per_hour: number; is_listed: boolean }>) =>
    request<Car>(`/v1/cars/${id}`, { method: 'PATCH', body: JSON.stringify(body) }),
  calendar: (carId: string, from: Date, to: Date) =>
    request<Calendar>(
      `/v1/cars/${carId}/calendar?from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}`,
    ),

  createSchedule: (body: {
    car_id: string;
    from: string;
    duration_minutes: number;
    period: 'weekly' | 'monthly' | 'yearly';
    timezone: string;
  }) => request<Schedule>('/v1/schedules', { method: 'POST', body: JSON.stringify(body) }),
  deleteSchedule: (id: string) => request<void>(`/v1/schedules/${id}`, { method: 'DELETE' }),
};

export function signIn(): void {
  window.location.href = '/v1/auth/google/login';
}
