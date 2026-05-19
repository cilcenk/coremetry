import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Spinner, Empty } from './Spinner';
import { api } from '@/lib/api';
import { fmtNum } from '@/lib/utils';
import { fmtSmart } from '@/lib/chartFmt';
import type { TimeRange } from '@/lib/types';
import { timeRangeToNs } from '@/lib/utils';

// TraceShapesView — v0.5.264. Renders the top-N trace-shape
// clusters for the current window + optional service pin.
// Each shape is the sorted-unique set of (service, operation)
// pairs in a trace; two traces share a shape iff they exercise
// the exact same touchpoints regardless of order or count.
//
// Counts are sampled at 10% trace_id hash so the underlying CH
// query stays under the 30s ceiling. The card surfaces a
// "sampled" tag so the operator doesn't read the count as exact.
// Click a card → /traces?... isn't navigable to a specific shape
// (the shape lives only on the wire), so the drill-down opens
// /traces with the dominant service from the signature as a
// filter. Good-enough first cohort drill.

export interface TraceShape {
  shapeId:      string;
  signature:    string[]; // "service|operation"
  traceCount:   number;
  avgMs:        number;
  p99Ms:        number;
  errorRate:    number;
  samplingRate: number;
}

export function TraceShapesView({ range, service }: {
  range: TimeRange;
  service?: string;
}) {
  const [shapes, setShapes] = useState<TraceShape[] | null | undefined>(undefined);
  const navigate = useNavigate();

  useEffect(() => {
    setShapes(undefined);
    const { from, to } = timeRangeToNs(range);
    fetch(`/api/traces/shapes?from=${from}&to=${to}${service ? `&service=${encodeURIComponent(service)}` : ''}`, {
      credentials: 'include',
    })
      .then(r => r.ok ? r.json() : null)
      .then(d => setShapes(d ?? null))
      .catch(() => setShapes(null));
  }, [range, service]);

  if (shapes === undefined) return <Spinner />;
  if (shapes === null) {
    return <Empty icon="!" title="Failed to load trace shapes" />;
  }
  if (shapes.length === 0) {
    return (
      <Empty icon="◇" title="No trace shapes in this window">
        Widen the time range, or remove the service filter.
      </Empty>
    );
  }

  // Sampling tag — all entries share the same rate; pulled from
  // the first row.
  const samplingRate = shapes[0]?.samplingRate ?? 1;
  const samplingPct = (samplingRate * 100).toFixed(0);

  return (
    <div>
      <div style={{
        marginBottom: 10, fontSize: 12, color: 'var(--text3)',
        display: 'flex', alignItems: 'center', gap: 12,
      }}>
        <span>
          <b style={{ color: 'var(--text)' }}>{shapes.length}</b> distinct shape{shapes.length === 1 ? '' : 's'}
          {' '}— traces grouped by their <code>(service, operation)</code> signature
        </span>
        {samplingRate < 1 && (
          <span style={{
            fontSize: 10, padding: '2px 6px', borderRadius: 10,
            background: 'rgba(250,204,21,0.10)',
            border: '1px solid rgba(250,204,21,0.40)',
            color: 'var(--warn, #facc15)',
            fontFamily: 'ui-monospace, monospace',
          }} title={`trace_id hash-sampled at ${samplingPct}% to keep the GROUP BY under the 30s query cap`}>
            Sampled at {samplingPct}%
          </span>
        )}
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {shapes.map(s => {
          const errCls = s.errorRate > 0.05 ? 'b-err' : s.errorRate > 0 ? 'b-warn' : 'b-ok';
          const errPct = (s.errorRate * 100).toFixed(2);
          // Pick a dominant service for the drill: just the first
          // signature entry's service half. Imperfect but
          // navigable.
          const drillService = s.signature[0]?.split('|')[0];
          return (
            <div key={s.shapeId} style={{
              background: 'var(--bg1)', border: '1px solid var(--border)',
              borderRadius: 6, padding: 12,
            }}>
              <div style={{
                display: 'flex', alignItems: 'baseline', gap: 12,
                marginBottom: 8,
              }}>
                <span style={{
                  fontSize: 18, fontWeight: 700, fontFamily: 'ui-monospace, monospace',
                  color: 'var(--accent2)',
                }}>~{fmtNum(s.traceCount)}</span>
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>traces</span>
                <span style={{ flex: 1 }} />
                <span className={`badge ${errCls}`} title={`${errPct}% errored`}>
                  {errPct}%
                </span>
                <span style={{
                  fontSize: 11, color: 'var(--text2)',
                  fontFamily: 'ui-monospace, monospace',
                }}>
                  avg <b>{fmtSmart(s.avgMs, 'ms')}</b>
                  {' · '}p99 <b>{fmtSmart(s.p99Ms, 'ms')}</b>
                </span>
                {drillService && (
                  <button className="sec"
                    onClick={() => navigate(`/traces?service=${encodeURIComponent(drillService)}`)}
                    style={{ fontSize: 11, padding: '2px 10px', whiteSpace: 'nowrap' }}>
                    Drill into {drillService} →
                  </button>
                )}
              </div>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                {s.signature.map(pair => {
                  const [svc, op] = pair.split('|', 2);
                  return (
                    <span key={pair} style={{
                      fontSize: 10, padding: '2px 6px', borderRadius: 10,
                      background: 'var(--bg2)', border: '1px solid var(--border)',
                      fontFamily: 'ui-monospace, monospace',
                      whiteSpace: 'nowrap',
                    }}
                    title={`service=${svc}, operation=${op}`}>
                      <span style={{ color: 'var(--text3)' }}>{svc}</span>
                      <span style={{ color: 'var(--text2)' }}> · </span>
                      <span style={{ color: 'var(--text)' }}>{op}</span>
                    </span>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
