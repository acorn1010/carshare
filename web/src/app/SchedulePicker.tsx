import { useEffect, useRef, useState } from 'react';
import { MonthGrid, TimeSelect } from './RangePicker';
import { Button, Plate } from './ui';

type Period = 'weekly' | 'monthly' | 'yearly';

const PERIODS: readonly Period[] = ['weekly', 'monthly', 'yearly'];

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] as const;
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'] as const;

const timeFormat = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' });

// How close together occurrences of each period can be, which caps how long
// one occurrence may run. Mirrors the API's validation.
const PERIOD_FLOOR_DAYS: Record<Period, number> = { weekly: 7, monthly: 28, yearly: 365 };

/** "Every Tuesday", "Every Friday – Sunday", "Monthly, the 4th – 6th",
 * "Every September 15". */
function cadence(from: Date, until: Date, period: Period): string {
  const multiDay = from.toDateString() !== until.toDateString();
  if (period === 'weekly') {
    return multiDay ? `Every ${WEEKDAYS[from.getDay()]} – ${WEEKDAYS[until.getDay()]}` : `Every ${WEEKDAYS[from.getDay()]}`;
  }
  if (period === 'monthly') {
    return multiDay ? `Monthly, the ${ordinal(from.getDate())} – ${ordinal(until.getDate())}` : `Monthly on the ${ordinal(from.getDate())}`;
  }
  return multiDay
    ? `Every ${MONTHS[from.getMonth()]} ${from.getDate()} – ${until.getDate()}`
    : `Every ${MONTHS[from.getMonth()]} ${from.getDate()}`;
}

function ordinal(day: number): string {
  const tens = day % 100;
  if (tens >= 11 && tens <= 13) {
    return `${day}th`;
  }
  const suffix = { 1: 'st', 2: 'nd', 3: 'rd' }[day % 10] ?? 'th';
  return `${day}${suffix}`;
}

/** One popover to define a repeating hold, in the trip picker's pattern. The
 * calendar picks a day or a span (click the first day, then the last, a
 * weekend trip is click Friday, click Sunday), times bound the first and last
 * day, cadence is a chip, and nothing happens until Add. */
export function SchedulePicker({ onAdd }: {
  readonly onAdd: (firstIso: string, durationMinutes: number, period: Period) => void;
}) {
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState<Date>(() => defaultStart(9));
  const [until, setUntil] = useState<Date>(() => defaultStart(11));
  const [picking, setPicking] = useState<'start' | 'end'>('start');
  const [period, setPeriod] = useState<Period>('weekly');
  const [viewYear, setViewYear] = useState(from.getFullYear());
  const [viewMonth, setViewMonth] = useState(from.getMonth());
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
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const pickDay = (day: Date) => {
    if (picking === 'start' || day.getTime() < startOfDay(from).getTime()) {
      setFrom(withDay(from, day));
      setUntil(clampEnd(withDay(from, day), withDay(until, day)));
      setPicking('end');
      return;
    }
    setUntil(clampEnd(from, withDay(until, day)));
    setPicking('start');
  };

  const durationMinutes = Math.round((until.getTime() - from.getTime()) / 60_000);
  const tooLong = durationMinutes >= PERIOD_FLOOR_DAYS[period] * 24 * 60;
  const nextTighterPeriod: Period | null = period === 'weekly' ? 'monthly' : period === 'monthly' ? 'yearly' : null;

  return (
    <div ref={root} className="relative">
      <Button
        tone="ghost"
        onClick={() => {
          setViewYear(from.getFullYear());
          setViewMonth(from.getMonth());
          setPicking('start');
          setOpen(!open);
        }}
      >
        + Add a repeating hold
      </Button>

      {open ? (
        <div className="animate-rise absolute z-1050 mt-2 w-max max-w-[94vw] rounded-2xl border border-paper-300 bg-paper-50 p-6 shadow-sheet motion-reduce:animate-none">
          <div className="flex items-start gap-4">
            <button type="button" onClick={() => shiftMonth(-1)} aria-label="Earlier" className="mt-1 rounded-lg px-3 py-1 text-lg font-bold hover:bg-paper-200 active:bg-paper-300">
              ‹
            </button>
            <MonthGrid year={viewYear} month={viewMonth} from={from} until={until} onPick={pickDay} />
            <button type="button" onClick={() => shiftMonth(1)} aria-label="Later" className="mt-1 rounded-lg px-3 py-1 text-lg font-bold hover:bg-paper-200 active:bg-paper-300">
              ›
            </button>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-3 border-t border-paper-200 pt-4">
            <TimeSelect label="Starts" value={minutesIntoDay(from)} onChange={(minutes) => {
              const next = withTime(from, minutes);
              setFrom(next);
              setUntil(clampEnd(next, until));
            }} />
            <TimeSelect label="Ends" value={minutesIntoDay(until)} onChange={(minutes) => {
              setUntil(clampEnd(from, withTime(until, minutes)));
            }} />
            <div className="flex items-center gap-1.5">
              <span className="pr-1 text-xs font-semibold uppercase tracking-wide text-paper-600">Repeats</span>
              {PERIODS.map((option) => (
                <button
                  key={option}
                  type="button"
                  onClick={() => setPeriod(option)}
                  className={`rounded-full px-3 py-1 text-xs font-bold transition-colors duration-75 ${
                    period === option
                      ? 'bg-paper-900 text-paper-50'
                      : 'bg-paper-200 text-paper-700 hover:bg-paper-300 active:bg-paper-400'
                  }`}
                >
                  {option}
                </button>
              ))}
            </div>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-paper-200 pt-4">
            {tooLong ? (
              <Plate tone="clay" className="text-sm">
                too long to repeat {period}{nextTighterPeriod ? `, try ${nextTighterPeriod}` : ''}
              </Plate>
            ) : (
              <Plate tone="marigold" className="text-sm">
                {cadence(from, until, period)} · {timeFormat.format(from)} – {timeFormat.format(until)}
              </Plate>
            )}
            <div className="ml-auto flex items-center gap-2">
              <Button tone="ghost" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button
                disabled={tooLong}
                onClick={() => {
                  onAdd(from.toISOString(), durationMinutes, period);
                  setOpen(false);
                }}
              >
                Add
              </Button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );

  function shiftMonth(delta: number) {
    const shifted = new Date(viewYear, viewMonth + delta, 1);
    setViewYear(shifted.getFullYear());
    setViewMonth(shifted.getMonth());
  }
}

function defaultStart(hour: number): Date {
  const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
  tomorrow.setHours(hour, 0, 0, 0);
  return tomorrow;
}

function startOfDay(date: Date): Date {
  const day = new Date(date);
  day.setHours(0, 0, 0, 0);
  return day;
}

function withDay(time: Date, day: Date): Date {
  const next = new Date(time);
  next.setFullYear(day.getFullYear(), day.getMonth(), day.getDate());
  return next;
}

function withTime(date: Date, minutesIntoDay: number): Date {
  const next = new Date(date);
  next.setHours(Math.floor(minutesIntoDay / 60), minutesIntoDay % 60, 0, 0);
  return next;
}

/** The hold ends at least 30 minutes after it starts. */
function clampEnd(from: Date, until: Date): Date {
  const minimum = from.getTime() + 30 * 60_000;
  return until.getTime() < minimum ? new Date(minimum) : until;
}

function minutesIntoDay(date: Date): number {
  return date.getHours() * 60 + Math.floor(date.getMinutes() / 30) * 30;
}
