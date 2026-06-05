// MiniWaterfall.tsx — inline span preview shown on a trace row expand.
//
// Fetches the real /api/traces/{id} (server-cached 5m; usually already warmed
// by the table's hover-prefetch), then renders up to 8 service-coloured bars
// positioned by start/duration within the trace window. Errors render red.
// "Open trace →" routes to the full waterfall. No fabricated data.

import { useEffect, useMemo, useState } from 'react';
import { api } from '@/lib/api';
import { spanHasError } from '@/lib/otel';
import type { SpanRow } from '@/lib/types';
import { svcColor, fmtDur } from './shared';

const TOP_N = 8;

export function MiniWaterfall({
  traceId, fallbackService, onOpen,
}: {
  traceId: string;
  fallbackService: string;
  onOpen: () => void;
}) {
  const [spans, setSpans] = useState<SpanRow[] | null | undefined>(undefined);

  useEffect(() => {
    let cancelled = false;
    setSpans(undefined);
    api.trace(traceId)
      .then(d => { if (!cancelled) setSpans(d?.spans ?? []); })
      .catch(() => { if (!cancelled) setSpans(null); });
    return () => { cancelled = true; };
  }, [traceId]);

  // Top-N longest spans, positioned within the full trace window so the
  // preview isn't dominated by tiny leaves.
  const view = useMemo(() => {
    if (!spans || spans.length === 0) return null;
    let t0 = Infinity, t1 = -Infinity;
    for (const s of spans) {
      if (s.startTime < t0) t0 = s.startTime;
      if (s.endTime > t1) t1 = s.endTime;
    }
    const total = Math.max(1, t1 - t0);
    const top = [...spans]
      .sort((a, b) => b.durationMs - a.durationMs)
      .slice(0, TOP_N)
      .sort((a, b) => a.startTime - b.startTime);
    return { t0, total, top };
  }, [spans]);

  return (
    <div style={{ padding: '8px 14px 12px 40px', background: 'var(--bg1)' }}>
      {spans === undefined && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '4px 0' }}>Loading spans…</div>
      )}
      {spans === null && (
        <div style={{ fontSize: 11, color: 'var(--err)', padding: '4px 0' }}>Could not load spans for this trace.</div>
      )}
      {spans && spans.length === 0 && (
        <div style={{ fontSize: 11, color: 'var(--text3)', padding: '4px 0' }}>
          No span detail (trace may have aged out of raw retention).
        </div>
      )}
      {view && view.top.map((s, i) => {
        const left = ((s.startTime - view.t0) / view.total) * 100;
        const width = Math.max(1.5, (Math.max(0, s.endTime - s.startTime) / view.total) * 100);
        const err = spanHasError(s);
        const svc = s.serviceName || fallbackService;
        return (
          <div key={s.spanId || i} style={{ display: 'grid', gridTemplateColumns: '220px 1fr', alignItems: 'center', height: 22, gap: 10 }}>
            <div style={{ display: 'flex', gap: 6, alignItems: 'center', overflow: 'hidden' }}>
              <span style={{ width: 6, height: 6, borderRadius: 6, background: svcColor(svc), flex: 'none' }} />
              <span className="mono" style={{ fontSize: 10.5, color: 'var(--text2)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {svc} · {s.name}
              </span>
            </div>
            <div style={{ position: 'relative', height: 12 }}>
              <div
                title={`${svc} · ${s.name} · ${fmtDur(s.durationMs)}`}
                style={{
                  position: 'absolute', left: `${left}%`, width: `${width}%`, height: 12,
                  borderRadius: 3, background: err ? 'var(--err)' : svcColor(svc), opacity: 0.85,
                }} />
            </div>
          </div>
        );
      })}
      <div style={{ marginTop: 6 }}>
        <a href={`/trace?id=${traceId}`}
          onClick={(e) => { e.preventDefault(); e.stopPropagation(); onOpen(); }}
          style={{ color: 'var(--accent2)', fontSize: 11.5, fontWeight: 600, textDecoration: 'none' }}>
          Open trace →
        </a>
      </div>
    </div>
  );
}
