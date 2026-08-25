/** Money and time formatting shared by every screen. */

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' });

/** Cents to "$12.50", with whole dollars losing the ".00". */
export function money(cents: number): string {
  const text = usd.format(cents / 100);
  return text.endsWith('.00') ? text.slice(0, -3) : text;
}

const dayTime = new Intl.DateTimeFormat('en-US', {
  weekday: 'short',
  month: 'short',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
});
const timeOnly = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' });

/** "Wed, Aug 26, 1:00 PM – 3:00 PM" when both ends share a day. */
export function window(startIso: string, endIso: string): string {
  const start = new Date(startIso);
  const end = new Date(endIso);
  const sameDay = start.toDateString() === end.toDateString();
  return `${dayTime.format(start)} – ${sameDay ? timeOnly.format(end) : dayTime.format(end)}`;
}

/** "420 m" below a kilometer, "1.8 km" above. */
export function distance(meters: number): string {
  return meters < 1000 ? `${Math.round(meters)} m` : `${(meters / 1000).toFixed(1)} km`;
}

/** Seconds to "9:58". */
export function countdown(totalSeconds: number): string {
  const clamped = Math.max(0, totalSeconds);
  return `${Math.floor(clamped / 60)}:${String(clamped % 60).padStart(2, '0')}`;
}

/** A datetime-local input value for a Date, in the user's zone. */
export function toLocalInput(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
