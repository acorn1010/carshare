import { useEffect, useRef, useState } from 'react';
import { Button, Plate } from './ui';

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'] as const;
const WEEKDAYS = ['S', 'M', 'T', 'W', 'T', 'F', 'S'] as const;
const MINUTES_STEP = 30;

const triggerFormat = new Intl.DateTimeFormat('en-US', {
  weekday: 'short',
  month: 'short',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
});

/** One picker for the whole trip: two months side by side, the range painted
 * across days, times in the same popover, a live trip-length chip, and
 * Save/Reset so nothing applies until the trip looks right. An end before the
 * start cannot be expressed: clicking earlier than the start restarts the
 * range, and times clamp. */
export function RangePicker({ from, until, onChange }: {
  readonly from: Date;
  readonly until: Date;
  readonly onChange: (from: Date, until: Date) => void;
}) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<{ from: Date; until: Date }>({ from, until });
  const [picking, setPicking] = useState<'start' | 'end'>('start');
  const [viewYear, setViewYear] = useState(from.getFullYear());
  const [viewMonth, setViewMonth] = useState(from.getMonth());
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const close = () => setOpen(false);
    const onDown = (event: MouseEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) {
        close();
      }
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        close();
      }
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const openFor = (mode: 'start' | 'end') => {
    setDraft({ from, until });
    setPicking(mode);
    setViewYear(from.getFullYear());
    setViewMonth(from.getMonth());
    setOpen(true);
  };

  const pickDay = (day: Date) => {
    if (picking === 'start' || day.getTime() < startOfDay(draft.from).getTime()) {
      const nextFrom = withDay(draft.from, day);
      setDraft({ from: nextFrom, until: clampEnd(nextFrom, withDay(draft.until, day)) });
      setPicking('end');
      return;
    }
    setDraft({ from: draft.from, until: clampEnd(draft.from, withDay(draft.until, day)) });
    setPicking('start');
  };

  const pickTime = (which: 'from' | 'until', minutesIntoDay: number) => {
    if (which === 'from') {
      const nextFrom = withTime(draft.from, minutesIntoDay);
      setDraft({ from: nextFrom, until: clampEnd(nextFrom, draft.until) });
      return;
    }
    setDraft({ from: draft.from, until: clampEnd(draft.from, withTime(draft.until, minutesIntoDay)) });
  };

  const shiftMonths = (delta: number) => {
    const shifted = new Date(viewYear, viewMonth + delta, 1);
    setViewYear(shifted.getFullYear());
    setViewMonth(shifted.getMonth());
  };

  const shown = open ? draft : { from, until };

  return (
    <div ref={root} className="relative">
      <div className="flex flex-wrap items-end gap-x-5 gap-y-4">
        <TriggerField label="From" value={triggerFormat.format(shown.from)} active={open && picking === 'start'} onClick={() => openFor('start')} />
        <TriggerField label="Until" value={triggerFormat.format(shown.until)} active={open && picking === 'end'} onClick={() => openFor('end')} />
      </div>

      {open ? (
        <div className="animate-rise absolute z-1050 mt-2 w-max max-w-[94vw] rounded-2xl border border-paper-300 bg-paper-50 p-6 shadow-sheet motion-reduce:animate-none">
          <div className="flex items-start gap-4">
            <button type="button" onClick={() => shiftMonths(-1)} aria-label="Earlier" className="mt-1 rounded-lg px-3 py-1 text-lg font-bold hover:bg-paper-200 active:bg-paper-300">
              ‹
            </button>
            <MonthGrid year={viewYear} month={viewMonth} from={draft.from} until={draft.until} onPick={pickDay} />
            <div className="hidden md:block">
              <MonthGrid {...nextMonth(viewYear, viewMonth)} from={draft.from} until={draft.until} onPick={pickDay} />
            </div>
            <button type="button" onClick={() => shiftMonths(1)} aria-label="Later" className="mt-1 rounded-lg px-3 py-1 text-lg font-bold hover:bg-paper-200 active:bg-paper-300">
              ›
            </button>
          </div>
          <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-3 border-t border-paper-200 pt-4">
            <TimeSelect label="Pick up" value={minutesIntoDay(draft.from)} onChange={(minutes) => pickTime('from', minutes)} />
            <TimeSelect label="Drop off" value={minutesIntoDay(draft.until)} onChange={(minutes) => pickTime('until', minutes)} />
            <Plate tone="pine" className="text-sm">
              {tripLength(draft.from, draft.until)}
            </Plate>
            <div className="ml-auto flex items-center gap-2">
              <Button
                tone="ghost"
                onClick={() => {
                  setDraft({ from, until });
                  setPicking('start');
                }}
              >
                Reset
              </Button>
              <Button
                onClick={() => {
                  onChange(draft.from, draft.until);
                  setOpen(false);
                }}
              >
                Save
              </Button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function TriggerField({ label, value, active, onClick }: {
  readonly label: string;
  readonly value: string;
  readonly active: boolean;
  readonly onClick: () => void;
}) {
  return (
    <div>
      <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">{label}</span>
      <button
        type="button"
        onClick={onClick}
        aria-expanded={active}
        className={`rounded-lg border px-3 py-2 text-sm font-semibold tabular-nums transition-colors duration-75 ${
          active ? 'border-pine-600 bg-paper-50' : 'border-paper-300 bg-paper-50 hover:border-paper-500 active:bg-paper-100'
        }`}
      >
        {value}
      </button>
    </div>
  );
}

function MonthGrid({ year, month, from, until, onPick }: {
  readonly year: number;
  readonly month: number;
  readonly from: Date;
  readonly until: Date;
  readonly onPick: (day: Date) => void;
}) {
  const firstWeekday = new Date(year, month, 1).getDay();
  const daysInMonth = new Date(year, month + 1, 0).getDate();
  const today = startOfDay(new Date());
  const rangeStart = startOfDay(from).getTime();
  const rangeEnd = startOfDay(until).getTime();

  return (
    <div className="w-80">
      <p className="pb-3 text-center text-base font-extrabold">
        {MONTHS[month]} {year}
      </p>
      <div className="grid grid-cols-7 gap-y-1 text-center">
        {WEEKDAYS.map((weekday, index) => (
          <span key={index} className="pb-1 text-xs font-bold uppercase text-paper-500">
            {weekday}
          </span>
        ))}
        {Array.from({ length: firstWeekday }, (_, index) => (
          <span key={`pad-${index}`} />
        ))}
        {Array.from({ length: daysInMonth }, (_, index) => {
          const day = new Date(year, month, index + 1);
          const time = day.getTime();
          const inRange = time >= rangeStart && time <= rangeEnd;
          const isEdge = time === rangeStart || time === rangeEnd;
          const isPast = day < today;
          return (
            <button
              key={index}
              type="button"
              disabled={isPast}
              onClick={() => onPick(day)}
              className={`mx-auto flex size-10 items-center justify-center text-sm font-semibold tabular-nums transition-colors duration-75 disabled:opacity-30 ${
                isEdge
                  ? 'rounded-full bg-pine-600 text-paper-50'
                  : inRange
                    ? 'w-full bg-pine-100 text-pine-800'
                    : 'rounded-full hover:bg-paper-200 active:bg-paper-300'
              }`}
            >
              {index + 1}
            </button>
          );
        })}
      </div>
    </div>
  );
}

function TimeSelect({ label, value, onChange }: {
  readonly label: string;
  readonly value: number;
  readonly onChange: (minutesIntoDay: number) => void;
}) {
  return (
    <label className="flex items-center gap-2">
      <span className="text-xs font-semibold uppercase tracking-wide text-paper-600">{label}</span>
      <select
        value={value}
        onChange={(event) => onChange(Number(event.target.value))}
        className="rounded-lg border border-paper-300 bg-paper-50 px-2.5 py-2 text-sm font-semibold tabular-nums"
      >
        {Array.from({ length: (24 * 60) / MINUTES_STEP }, (_, step) => {
          const minutes = step * MINUTES_STEP;
          const hour = Math.floor(minutes / 60);
          const displayHour = hour % 12 === 0 ? 12 : hour % 12;
          return (
            <option key={minutes} value={minutes}>
              {displayHour}:{String(minutes % 60).padStart(2, '0')} {hour < 12 ? 'AM' : 'PM'}
            </option>
          );
        })}
      </select>
    </label>
  );
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

/** The invariant: the trip ends at least 30 minutes after it starts. */
function clampEnd(from: Date, until: Date): Date {
  const minimum = from.getTime() + MINUTES_STEP * 60_000;
  return until.getTime() < minimum ? new Date(minimum) : until;
}

function minutesIntoDay(date: Date): number {
  return date.getHours() * 60 + Math.floor(date.getMinutes() / MINUTES_STEP) * MINUTES_STEP;
}

function tripLength(from: Date, until: Date): string {
  const totalMinutes = Math.round((until.getTime() - from.getTime()) / 60_000);
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;
  const parts: string[] = [];
  if (days > 0) {
    parts.push(`${days}d`);
  }
  if (hours > 0) {
    parts.push(`${hours}h`);
  }
  if (minutes > 0 && days === 0) {
    parts.push(`${minutes}m`);
  }
  return parts.join(' ') || '0m';
}

function nextMonth(year: number, month: number): { year: number; month: number } {
  const date = new Date(year, month + 1, 1);
  return { year: date.getFullYear(), month: date.getMonth() };
}
