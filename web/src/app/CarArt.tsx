/** Flat car silhouette, tinted deterministically per car so the list reads as
 * a fleet of different cars without shipping photos. */

const TINTS = ['#16714a', '#c98317', '#c94e32', '#2f2b22', '#47ab7c', '#6b6350'] as const;

function tintFor(id: string): string {
  let hash = 0;
  for (const char of id) {
    hash = (hash * 31 + char.charCodeAt(0)) | 0;
  }
  return TINTS[Math.abs(hash) % TINTS.length]!;
}

export function CarArt({ carId, className = '' }: { readonly carId: string; readonly className?: string }) {
  const tint = tintFor(carId);
  return (
    <svg viewBox="0 0 120 48" className={className} aria-hidden="true">
      <path
        d="M8 36 C8 30 12 27 20 26 L32 24 C40 15 52 11 64 11 C77 11 87 15 95 23 L106 25 C112 26 114 29 114 33 L114 36 C114 38 112 39 110 39 L103 39 A11 11 0 0 0 81 39 L41 39 A11 11 0 0 0 19 39 L12 39 C9 39 8 38 8 36 Z"
        fill={tint}
      />
      <path d="M44 23 C50 16 58 14 64 14 C71 14 77 16 82 21 L82 24 L44 24 Z" fill="#f5f1e8" opacity="0.85" />
      <circle cx="30" cy="38" r="7" fill="#201d17" />
      <circle cx="30" cy="38" r="3" fill="#f5f1e8" />
      <circle cx="92" cy="38" r="7" fill="#201d17" />
      <circle cx="92" cy="38" r="3" fill="#f5f1e8" />
    </svg>
  );
}
