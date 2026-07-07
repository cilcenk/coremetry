import { useMemo } from 'react';
import { displaySpanName } from '@/lib/utils';
import type { OperationSummary, TimeRange } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';

// RpsByOperation — v0.8.370 (operator-requested on the mockup
// review): per-operation request rate as a compact bar list on the
// Details tab. ZERO new fetches — the page already holds the
// operations summary for the Operations tab; rate = spanCount over
// the selected window. Top 8 by rate, bars normalized to the
// busiest operation, error-tinted when the op's error rate is
// meaningful (>1%).

export function RpsByOperation({ operations, range, onOpenOperations }: {
  operations: OperationSummary[];
  range: TimeRange;
  onOpenOperations: () => void;
}) {
  const windowSec = useMemo(() => {
    const { from, to } = timeRangeToNs(range);
    return Math.max(1, (to - from) / 1e9);
  }, [range]);

  const top = useMemo(() =>
    [...operations]
      .sort((a, b) => b.spanCount - a.spanCount)
      .slice(0, 8)
      .map(o => ({
        name: displaySpanName({ name: o.name }),
        rps: o.spanCount / windowSec,
        errRate: o.errorRate,
      })),
    [operations, windowSec]);

  if (top.length === 0) return null;
  const max = top[0].rps || 1;

  return (
    <div className="card" style={{ padding: '11px 14px 13px' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 8 }}>
        <b style={{ fontSize: 12.5 }}>RPS by operation</b>
        <a href="#" onClick={e => { e.preventDefault(); onOpenOperations(); }}
          style={{ color: 'var(--accent)', fontSize: 11 }}>operations →</a>
      </div>
      {top.map(o => (
        <div key={o.name} style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) 120px 64px', gap: 10, alignItems: 'center', padding: '3px 0' }}>
          <span className="mono" style={{ fontSize: 11.5, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={o.name}>
            {o.name}
          </span>
          <span style={{ height: 6, background: 'var(--bg3)', borderRadius: 2, overflow: 'hidden' }}>
            <span style={{
              display: 'block', height: '100%', width: `${Math.max(2, (o.rps / max) * 100)}%`,
              background: o.errRate > 1 ? 'var(--err)' : 'var(--accent)', opacity: .75,
            }} />
          </span>
          <span className="mono" style={{ fontSize: 11.5, textAlign: 'right', color: 'var(--text2)' }}>
            {o.rps >= 10 ? o.rps.toFixed(0) : o.rps.toFixed(1)}/s
          </span>
        </div>
      ))}
    </div>
  );
}
