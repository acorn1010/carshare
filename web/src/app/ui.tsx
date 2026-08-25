import { useCallback, useRef, useState, type ReactNode } from 'react';

/** The license-plate price chip, the interface's signature mark. */
export function Plate({ children, tone = 'ink', className = '' }: {
  readonly children: ReactNode;
  readonly tone?: 'ink' | 'pine' | 'marigold' | 'clay';
  readonly className?: string;
}) {
  const tones = {
    ink: 'text-paper-900 bg-paper-50',
    pine: 'text-paper-50 bg-pine-600 border-pine-800!',
    marigold: 'text-marigold-700 bg-marigold-100',
    clay: 'text-clay-700 bg-clay-100',
  } as const;
  return <span className={`plate ${tones[tone]} ${className}`}>{children}</span>;
}

export function Button({ children, onClick, tone = 'pine', disabled, className = '' }: {
  readonly children: ReactNode;
  readonly onClick?: () => void;
  readonly tone?: 'pine' | 'ghost' | 'clay';
  readonly disabled?: boolean;
  readonly className?: string;
}) {
  const tones = {
    pine: 'bg-pine-600 text-paper-50 hover:bg-pine-700 active:bg-pine-800',
    ghost: 'bg-transparent text-paper-800 border border-paper-300 hover:border-paper-500 hover:bg-paper-50',
    clay: 'bg-transparent text-clay-600 border border-clay-500/40 hover:bg-clay-100',
  } as const;
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={`rounded-lg px-4 py-2 text-sm font-semibold transition-colors duration-75 disabled:cursor-not-allowed disabled:opacity-40 ${tones[tone]} ${className}`}
    >
      {children}
    </button>
  );
}

export function Chip({ children, tone }: {
  readonly children: ReactNode;
  readonly tone: 'pine' | 'marigold' | 'clay' | 'paper';
}) {
  const tones = {
    pine: 'bg-pine-100 text-pine-800',
    marigold: 'bg-marigold-100 text-marigold-700',
    clay: 'bg-clay-100 text-clay-700',
    paper: 'bg-paper-200 text-paper-700',
  } as const;
  return (
    <span className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-semibold uppercase tracking-wide ${tones[tone]}`}>
      {children}
    </span>
  );
}

/** The stamped verdict mark: BOOKED in pine, JUST TAKEN in clay. */
export function Stamp({ children, tone }: {
  readonly children: ReactNode;
  readonly tone: 'pine' | 'clay';
}) {
  const tones = { pine: 'text-pine-600 border-pine-600', clay: 'text-clay-600 border-clay-600' } as const;
  return (
    <span
      className={`motion-reduce:animate-none animate-stamp inline-block rounded border-3 px-3 py-1 text-xl font-extrabold uppercase tracking-widest ${tones[tone]}`}
    >
      {children}
    </span>
  );
}

type Toast = { readonly text: string; readonly tone: 'pine' | 'clay' };

/** One toast at a time, bottom center, stamped look for failures. */
export function useToast(): { toast: ReactNode; show: (text: string, tone?: 'pine' | 'clay') => void } {
  const [toast, setToast] = useState<Toast | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout>>(null);
  const show = useCallback((text: string, tone: 'pine' | 'clay' = 'clay') => {
    setToast({ text, tone });
    if (timer.current) {
      clearTimeout(timer.current);
    }
    timer.current = setTimeout(() => setToast(null), 3600);
  }, []);
  const node = toast ? (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-1100 flex justify-center">
      <div
        className={`animate-rise rounded-xl border-2 bg-paper-50 px-4 py-2.5 text-sm font-bold shadow-sheet ${
          toast.tone === 'clay' ? 'border-clay-600 text-clay-700' : 'border-pine-600 text-pine-800'
        }`}
      >
        {toast.text}
      </div>
    </div>
  ) : null;
  return { toast: node, show };
}
