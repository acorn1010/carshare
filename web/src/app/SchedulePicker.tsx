import { useEffect, useRef, useState } from 'react';
import { MonthGrid, TimeSelect } from './RangePicker';
import { Button, Plate } from './ui';

type Period = 'weekly' | 'monthly' | 'yearly';

const DURATIONS = [
  { minutes: 60, label: '1h' },
  { minutes: 120, label: '2h' },
  { minutes: 240, label: '4h' },
  { minutes: 480, label: '8h' },
] as const;
const PERIODS: readonly Period[] = ['weekly', 'monthly', 'yearly'];

const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'] as const;
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'] as const;

const timeFormat = new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' });

/** "Every Tuesday", "Monthly on the 15th", "Every September 15". */
function cadence(first: Date, period: Period): string {
  if (period === 'weekly') {
    return `Every ${WEEKDAYS[first.getDay()]}`;
  }
  if (period === 'monthly') {
    return `Monthly on the ${first.getDate()}`;
  }
  return `Every ${MONTHS[first.getMonth()]} ${first.getDate()}`;
}

/** One popover to define a repeating hold, in the trip picker's pattern:
 * calendar for the first occurrence, start time, duration and cadence as
 * chips, a live plain-words summary, and nothing happens until Add. */
export function SchedulePicker({ onAdd }: {
  readonly onAdd: (firstIso: string, durationMinutes: number, period: Period) => void;
}) {
  const [open, setOpen] = useState(false);
  const [first, setFirst] = useState<Date>(() => {
    const tomorrow = new Date(Date.now() + 24 * 3600 * 1000);
    tomorrow.setHours(9, 0, 0, 0);
    return tomorrow;
  });
  const [durationMinutes, setDurationMinutes] = useState(120);
  const [period, setPeriod] = useState<Period>('weekly');
  const [viewYear, setViewYear] = useState(first.getFullYear());
  const [viewMonth, setViewMonth] = useState(first.getMonth());
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
    const next = new Date(first);
    next.setFullYear(day.getFullYear(), day.getMonth(), day.getDate());
    setFirst(next);
  };
  const pickTime = (minutesIntoDay: number) => {
    const next = new Date(first);
    next.setHours(Math.floor(minutesIntoDay / 60), minutesIntoDay % 60, 0, 0);
    setFirst(next);
  };
  const shiftMonth = (delta: number) => {
    const shifted = new Date(viewYear, viewMonth + delta, 1);
    setViewYear(shifted.getFullYear());
    setViewMonth(shifted.getMonth());
  };

  return (
    <div ref={root} className="relative">
      <Button
        tone="ghost"
        onClick={() => {
          setViewYear(first.getFullYear());
          setViewMonth(first.getMonth());
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
            <MonthGrid year={viewYear} month={viewMonth} from={first} until={first} onPick={pickDay} />
            <button type="button" onClick={() => shiftMonth(1)} aria-label="Later" className="mt-1 rounded-lg px-3 py-1 text-lg font-bold hover:bg-paper-200 active:bg-paper-300">
              ›
            </button>
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-3 border-t border-paper-200 pt-4">
            <TimeSelect label="Starts" value={first.getHours() * 60 + first.getMinutes()} onChange={pickTime} />
            <ChipRow options={DURATIONS.map((option) => ({ key: String(option.minutes), label: option.label }))}
              selected={String(durationMinutes)} onSelect={(key) => setDurationMinutes(Number(key))} label="For" />
            <ChipRow options={PERIODS.map((option) => ({ key: option, label: option }))}
              selected={period} onSelect={(key) => setPeriod(key as Period)} label="Repeats" />
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-paper-200 pt-4">
            <Plate tone="marigold" className="text-sm">
              {cadence(first, period)} · {timeFormat.format(first)}
            </Plate>
            <div className="ml-auto flex items-center gap-2">
              <Button tone="ghost" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button
                onClick={() => {
                  onAdd(first.toISOString(), durationMinutes, period);
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
}

function ChipRow({ label, options, selected, onSelect }: {
  readonly label: string;
  readonly options: readonly { readonly key: string; readonly label: string }[];
  readonly selected: string;
  readonly onSelect: (key: string) => void;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="pr-1 text-xs font-semibold uppercase tracking-wide text-paper-600">{label}</span>
      {options.map((option) => (
        <button
          key={option.key}
          type="button"
          onClick={() => onSelect(option.key)}
          className={`rounded-full px-3 py-1 text-xs font-bold transition-colors duration-75 ${
            selected === option.key
              ? 'bg-paper-900 text-paper-50'
              : 'bg-paper-200 text-paper-700 hover:bg-paper-300 active:bg-paper-400'
          }`}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}
