// ServiceTimeline.tsx — the per-service density mini-waterfall, extracted from
// TracePeekDrawer (v0.5.398) so BOTH the trace-peek drawer and the new
// CorrelationContextDrawer (task #6) render the SAME timeline without
// duplicating the geometry. Each row = a service; the bar shows the span window
// that service touched, relative to the trace's overall start..end. This is a
// density bar (not a full waterfall — that's the dedicated /trace page); Tempo's
// "service timeline" panel uses the same compression.

import { useMemo } from 'react';
import { fmtNum } from '@/lib/utils';
import type { SpanRow } from '@/lib/types';

// ServiceTimeline renders one density row per service, ordered by first
// appearance so the visual reads top→down as the trace's call sequence.
// Self-contained: derives its own min/max + per-service windows from `spans`.
export function ServiceTimeline({ spans }: { spans: SpanRow[] }) {
  const model = useMemo(() => {
    if (!spans || spans.length === 0) return null;
    const minStart = spans.reduce((m, s) => Math.min(m, s.startTime), Infinity);
    const maxEnd = spans.reduce((m, s) => Math.max(m, s.endTime), 0);
    // First-seen ordering → the call sequence reads top→down.
    const firstSeen = new Map<string, number>();
    for (const s of spans) {
      if (!firstSeen.has(s.serviceName)) firstSeen.set(s.serviceName, s.startTime);
    }
    const orderedServices = Array.from(firstSeen.entries())
      .sort((a, b) => a[1] - b[1])
      .map(([n]) => n);
    return { minStart, maxEnd, orderedServices };
  }, [spans]);

  if (!model) return null;
  const { minStart, maxEnd, orderedServices } = model;
  const totalNs = maxEnd - minStart;

  return (
    <>
      {orderedServices.map((svc) => {
        const svcSpans = spans.filter((s) => s.serviceName === svc);
        const svcStart = Math.min(...svcSpans.map((s) => s.startTime));
        const svcEnd = Math.max(...svcSpans.map((s) => s.endTime));
        const left = totalNs > 0 ? ((svcStart - minStart) / totalNs) * 100 : 0;
        const width = totalNs > 0 ? Math.max(0.5, ((svcEnd - svcStart) / totalNs) * 100) : 100;
        const svcErrs = svcSpans.filter((s) => s.statusCode === 'error').length;
        const barColor = svcErrs > 0 ? 'var(--err)' : 'var(--accent2)';
        return (
          <div
            key={svc}
            style={{
              display: 'grid',
              gridTemplateColumns: '140px 1fr 80px',
              gap: 6,
              alignItems: 'center',
              marginBottom: 3,
              fontSize: 11,
            }}>
            <span
              className="mono"
              style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
              title={svc}>
              {svc}
            </span>
            <div style={{ position: 'relative', height: 12, background: 'var(--bg2)', borderRadius: 2 }}>
              <div
                style={{
                  position: 'absolute',
                  top: 0,
                  bottom: 0,
                  left: `${left}%`,
                  width: `${width}%`,
                  background: barColor,
                  borderRadius: 2,
                  opacity: 0.85,
                }}
              />
            </div>
            <span className="mono" style={{ fontSize: 10, color: 'var(--text3)', textAlign: 'right' }}>
              {fmtNum(svcSpans.length)} sp · {((svcEnd - svcStart) / 1e6).toFixed(0)}ms
            </span>
          </div>
        );
      })}
    </>
  );
}
