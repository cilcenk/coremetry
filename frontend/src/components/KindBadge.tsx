import type { ProfileFrameKind, ProfileCategoryBreakdown } from '@/lib/types';

// Shared visual language for frame kinds. Operators learn one
// colour mapping and read both the per-row badges and the
// breakdown bar without re-mapping in their head.
//
// CPU   → green   (work is happening)
// Lock  → red     (contention — first thing to chase on a perf bug)
// IO    → blue    (syscall / network wait)
// Sleep → grey    (intentional pause)
// GC    → orange  (runtime overhead — JVM GC, Go gc, etc.)
// Token-mapped (theme-adaptive): the raw values were an exact match for
// these globals.css tokens, so the badge now flips correctly in light/dark.
const COLORS: Record<ProfileFrameKind, string> = {
  cpu:   'var(--ok)',
  lock:  'var(--brand)',
  io:    'var(--accent)',
  sleep: 'var(--text2)',
  gc:    'var(--orange)',
};

const LABELS: Record<ProfileFrameKind, string> = {
  cpu:   'CPU',
  lock:  'Lock',
  io:    'IO',
  sleep: 'Sleep',
  gc:    'GC',
};

export function kindColor(k: ProfileFrameKind): string { return COLORS[k]; }
export function kindLabel(k: ProfileFrameKind): string { return LABELS[k]; }

export function KindBadge({ kind }: { kind: ProfileFrameKind }) {
  if (kind === 'cpu') return null; // CPU is the default; reduce visual noise
  const c = COLORS[kind];
  return (
    <span style={{
      fontSize: 10, fontWeight: 700, padding: '1px 6px',
      marginLeft: 6,
      background: c, color: 'white',
      borderRadius: 3, fontFamily: 'monospace',
      verticalAlign: 'middle',
    }}>
      {LABELS[kind]}
    </span>
  );
}

// BreakdownBar — stacked horizontal bar showing the leaf-time
// distribution across kinds. Sits at the top of the hotspot
// view so the operator sees at a glance whether a slow service
// is CPU-bound, lock-bound, or IO-bound BEFORE drilling into
// individual methods.
export function BreakdownBar({ b }: { b: ProfileCategoryBreakdown | undefined }) {
  if (!b) return null;
  const total = b.cpu + b.lock + b.io + b.sleep + b.gc;
  if (total <= 0) return null;
  const order: ProfileFrameKind[] = ['cpu', 'lock', 'io', 'sleep', 'gc'];
  const pct = (n: number) => (n / total) * 100;
  return (
    <div style={{
      marginBottom: 12, padding: 10, borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
    }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 6 }}>
        Leaf-time breakdown — where each sample landed
      </div>
      <div style={{ display: 'flex', height: 16, borderRadius: 4, overflow: 'hidden',
                    border: '1px solid var(--border)' }}>
        {order.map(k => {
          const w = pct(b[k]);
          if (w <= 0) return null;
          return (
            <div key={k} title={`${LABELS[k]}: ${w.toFixed(1)}%`} style={{
              width: w + '%', background: COLORS[k], minWidth: 1,
            }} />
          );
        })}
      </div>
      <div style={{ display: 'flex', gap: 14, marginTop: 8, fontSize: 11, flexWrap: 'wrap' }}>
        {order.map(k => {
          const w = pct(b[k]);
          if (w <= 0) return null;
          return (
            <span key={k} style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              <span style={{ width: 8, height: 8, borderRadius: 2, background: COLORS[k] }} />
              <b style={{ color: 'var(--text)' }}>{LABELS[k]}</b>
              <span style={{ color: 'var(--text2)' }}>{w.toFixed(1)}%</span>
            </span>
          );
        })}
      </div>
    </div>
  );
}
