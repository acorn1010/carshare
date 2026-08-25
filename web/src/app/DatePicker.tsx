import { useEffect, useRef, useState } from 'react';

const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'] as const;
const WEEKDAYS = ['S', 'M', 'T', 'W', 'T', 'F', 'S'] as const;

const triggerFormat = new Intl.DateTimeFormat('en-US', {
  weekday: 'short',
  month: 'short',
  day: 'numeric',
  hour: 'numeric',
  minute: '2-digit',
});

/** Date and time picker in the house style: a calendar popover plus 30-minute
 * time slots, replacing the browser's stock datetime control. */
export function DatePicker({ value, onChange, label }: {
  readonly value: Date;
  readonly onChange: (next: Date) => void;
  readonly label: string;
}) {
  const [open, setOpen] = useState(false);
  const [viewYear, setViewYear] = useState(value.getFullYear());
  const [viewMonth, setViewMonth] = useState(value.getMonth());
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

  const firstWeekday = new Date(viewYear, viewMonth, 1).getDay();
  const daysInMonth = new Date(viewYear, viewMonth + 1, 0).getDate();
  const today = new Date();
  today.setHours(0, 0, 0, 0);

  const pickDay = (day: number) => {
    const next = new Date(value);
    next.setFullYear(viewYear, viewMonth, day);
    onChange(next);
  };
  const pickTime = (halfHours: number) => {
    const next = new Date(value);
    next.setHours(Math.floor(halfHours / 2), (halfHours % 2) * 30, 0, 0);
    onChange(next);
  };
  const shiftMonth = (delta: number) => {
    const shifted = new Date(viewYear, viewMonth + delta, 1);
    setViewYear(shifted.getFullYear());
    setViewMonth(shifted.getMonth());
  };

  return (
    <div ref={root} className="relative">
      <span className="mb-1 block text-xs font-semibold uppercase tracking-wide text-paper-600">{label}</span>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="rounded-lg border border-paper-300 bg-paper-50 px-3 py-2 text-sm font-semibold tabular-nums hover:border-paper-500"
      >
        {triggerFormat.format(value)}
      </button>

      {open ? (
        <div className="animate-rise absolute z-1050 mt-2 flex gap-3 rounded-xl border border-paper-300 bg-paper-50 p-3 shadow-sheet motion-reduce:animate-none">
          <div className="w-56">
            <div className="flex items-center justify-between pb-2">
              <button type="button" onClick={() => shiftMonth(-1)} aria-label="Previous month" className="rounded-md px-2 py-0.5 font-bold hover:bg-paper-200">
                ‹
              </button>
              <span className="text-sm font-extrabold">
                {MONTHS[viewMonth]} {viewYear}
              </span>
              <button type="button" onClick={() => shiftMonth(1)} aria-label="Next month" className="rounded-md px-2 py-0.5 font-bold hover:bg-paper-200">
                ›
              </button>
            </div>
            <div className="grid grid-cols-7 gap-0.5 text-center">
              {WEEKDAYS.map((weekday, index) => (
                <span key={index} className="text-[10px] font-bold uppercase text-paper-500">
                  {weekday}
                </span>
              ))}
              {Array.from({ length: firstWeekday }, (_, index) => (
                <span key={`pad-${index}`} />
              ))}
              {Array.from({ length: daysInMonth }, (_, index) => {
                const day = index + 1;
                const date = new Date(viewYear, viewMonth, day);
                const isSelected =
                  value.getFullYear() === viewYear && value.getMonth() === viewMonth && value.getDate() === day;
                const isPast = date < today;
                return (
                  <button
                    key={day}
                    type="button"
                    disabled={isPast}
                    onClick={() => pickDay(day)}
                    className={`rounded-md py-1 text-xs font-semibold tabular-nums transition-colors duration-75 disabled:opacity-30 ${
                      isSelected ? 'bg-pine-600 text-paper-50' : 'hover:bg-paper-200'
                    }`}
                  >
                    {day}
                  </button>
                );
              })}
            </div>
          </div>
          <ul className="h-56 w-24 overflow-y-auto rounded-lg border border-paper-200">
            {Array.from({ length: 48 }, (_, halfHours) => {
              const hour = Math.floor(halfHours / 2);
              const minute = (halfHours % 2) * 30;
              const isSelected = value.getHours() === hour && value.getMinutes() === minute;
              const meridiem = hour < 12 ? 'AM' : 'PM';
              const displayHour = hour % 12 === 0 ? 12 : hour % 12;
              return (
                <li key={halfHours}>
                  <button
                    type="button"
                    onClick={() => pickTime(halfHours)}
                    className={`w-full px-2 py-1 text-center text-xs font-semibold tabular-nums transition-colors duration-75 ${
                      isSelected ? 'bg-pine-600 text-paper-50' : 'hover:bg-paper-200'
                    }`}
                  >
                    {displayHour}:{String(minute).padStart(2, '0')} {meridiem}
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
