import { Suspense, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { computeCriticalPath } from '@/lib/criticalPath';
import { api } from '@/lib/api';
import { fmtNs } from '@/lib/utils';
import type { SpanRow, TraceDetailResponse } from '@/lib/types';

// /trace/compare?a=<traceId>&b=<traceId>
//
// Side-by-side waterfall view for performance regression
// debugging. The two traces load in parallel via two useQuery
// calls; the page renders nothing until either both arrive or
// one errors. Each side gets its OWN computed critical path so
// the highlight reflects what was slow in THAT trace, not the
// other.
//
// Header summary: per-side total duration + the delta (B − A)
// in ms with a colour cue (red = slower, green = faster). For
// the operator that's the headline number — "is the new build
// faster or slower, by how much".
//
// Span matching across traces (advanced — not in this round):
// future work could align spans by name+depth so the operator
// sees per-operation deltas. For now the side-by-side
// waterfalls are independent; the operator scans both at the
// same x-scale to spot the divergent operation.
//
// Performance: each useQuery caches per traceId; tabbing back
// to the same comparison is instant. TraceWaterfall already
// virtualises long traces internally; rendering two of them
// is ~2x the cost of one, not the (#A × #B) the naive merge
// would have.

export default function TraceComparePage() {
  return <Suspense fallback={<Spinner />}><Inner /></Suspense>;
}

function Inner() {
  const [sp] = useSearchParams();
  const a = sp.get('a') ?? '';
  const b = sp.get('b') ?? '';

  const aQ = useQuery<TraceDetailResponse>({
    queryKey: ['trace', a],
    queryFn: () => api.trace(a),
    enabled: !!a,
    staleTime: 5 * 60_000, // traces are immutable; cache aggressively
  });
  const bQ = useQuery<TraceDetailResponse>({
    queryKey: ['trace', b],
    queryFn: () => api.trace(b),
    enabled: !!b,
    staleTime: 5 * 60_000,
  });

  // Local input state for the second-trace picker. Operators
  // typically open compare from a single trace ("Compare ↔"
  // button) and then paste the other ID into this field.
  const [bDraft, setBDraft] = useState(b);

  if (!a) {
    return (
      <>
        <Topbar title="Trace compare" />
        <div id="content">
          <Empty icon="↔" title="No trace selected">
            Open a trace and click <b>Compare ↔</b> to start a side-by-side comparison.
          </Empty>
        </div>
      </>
    );
  }

  return (
    <>
      <Topbar title="Trace compare" />
      <div id="content">
        <div className="controls" style={{ marginBottom: 12, alignItems: 'center' }}>
          <span style={{ fontSize: 12, color: 'var(--text2)' }}>
            Trace A:{' '}
            <Link to={`/trace?id=${encodeURIComponent(a)}`}
                  style={{ fontFamily: 'monospace' }}>
              {a.slice(0, 12)}…
            </Link>
          </span>
          <span style={{ fontSize: 12, color: 'var(--text2)', marginLeft: 12 }}>
            Trace B:
          </span>
          <input value={bDraft}
                 onChange={e => setBDraft(e.target.value.trim())}
                 placeholder="paste trace ID…"
                 style={{ width: 260, fontFamily: 'monospace', fontSize: 12 }} />
          <Link to={`/trace/compare?a=${encodeURIComponent(a)}&b=${encodeURIComponent(bDraft)}`}
                aria-disabled={!bDraft}
                className="sec"
                style={{
                  fontSize: 12, padding: '3px 10px',
                  textDecoration: 'none', color: 'var(--text)',
                  border: '1px solid var(--border)', borderRadius: 6,
                  opacity: bDraft ? 1 : 0.5,
                  pointerEvents: bDraft ? 'auto' : 'none',
                }}>
            Load
          </Link>
          <span style={{ marginLeft: 'auto' }} />
        </div>

        {!b && (
          <Empty icon="↔" title="Pick a second trace">
            Paste a trace ID to compare against <code>{a.slice(0, 12)}…</code>.
            The two will render side-by-side with their own critical paths
            highlighted, so a regression jumps off the page.
          </Empty>
        )}

        {b && (
          <div style={{
            display: 'grid',
            gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)',
            gap: 12,
          }}>
            <TraceSide label="A" id={a} q={aQ} otherQ={bQ} />
            <TraceSide label="B" id={b} q={bQ} otherQ={aQ} />
          </div>
        )}
      </div>
    </>
  );
}

function TraceSide({ label, id, q, otherQ }: {
  label: 'A' | 'B';
  id: string;
  q: ReturnType<typeof useQuery<TraceDetailResponse>>;
  otherQ: ReturnType<typeof useQuery<TraceDetailResponse>>;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const spans: SpanRow[] = q.data?.spans ?? [];
  const totalNs = useMemo(() => {
    if (spans.length === 0) return 0;
    const minT = Math.min(...spans.map(s => s.startTime));
    const maxT = Math.max(...spans.map(s => s.endTime));
    return maxT - minT;
  }, [spans]);

  const critical = useMemo(() => {
    if (spans.length === 0) return null;
    return computeCriticalPath(spans.map(s => ({
      spanId: s.spanId,
      parentId: s.parentSpanId ?? '',
      startTime: s.startTime,
      duration: s.endTime - s.startTime,
    })));
  }, [spans]);

  // Delta vs the other side — only meaningful once both
  // queries have data. Positive (B − A) > 0 → B is slower (red);
  // < 0 → B faster (green); 0 → identical.
  const otherSpans: SpanRow[] = otherQ.data?.spans ?? [];
  const otherTotalNs = useMemo(() => {
    if (otherSpans.length === 0) return 0;
    const minT = Math.min(...otherSpans.map(s => s.startTime));
    const maxT = Math.max(...otherSpans.map(s => s.endTime));
    return maxT - minT;
  }, [otherSpans]);
  const deltaNs = label === 'B' && otherTotalNs > 0 ? totalNs - otherTotalNs : 0;
  const deltaColor = deltaNs > 0 ? 'var(--err)' : deltaNs < 0 ? 'var(--ok)' : 'var(--text3)';

  return (
    <div style={{ minWidth: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 6, padding: 10,
        display: 'flex', alignItems: 'baseline', gap: 10, flexWrap: 'wrap',
      }}>
        <span style={{ fontSize: 14, fontWeight: 700 }}>Trace {label}</span>
        <Link to={`/trace?id=${encodeURIComponent(id)}`}
              style={{ fontFamily: 'monospace', fontSize: 11 }}>
          {id.slice(0, 12)}…
        </Link>
        {q.isLoading && <span style={{ color: 'var(--text3)', fontSize: 12 }}>loading…</span>}
        {q.isError && <span style={{ color: 'var(--err)', fontSize: 12 }}>load failed</span>}
        {q.data && (
          <>
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>
              {spans.length} spans · {fmtNs(totalNs)}
            </span>
            {critical && (
              <span style={{ color: 'var(--text3)', fontSize: 11 }}>
                critical {fmtNs(critical.totalNs)}
              </span>
            )}
            {label === 'B' && otherQ.data && (
              <span style={{ marginLeft: 'auto', color: deltaColor, fontSize: 12, fontWeight: 600 }}>
                {deltaNs >= 0 ? '+' : '−'}{fmtNs(Math.abs(deltaNs))}
                <span style={{ color: 'var(--text3)', fontWeight: 400, marginLeft: 4 }}>
                  vs A
                </span>
              </span>
            )}
          </>
        )}
      </div>
      {q.data && spans.length > 0 && (
        <div style={{ height: 'calc(100vh - 220px)', overflow: 'auto',
                       border: '1px solid var(--border)', borderRadius: 6 }}>
          <TraceWaterfall spans={spans} selectedId={selected} onSelect={setSelected}
                          criticalPathIds={critical?.ids} />
        </div>
      )}
      {q.isLoading && <Spinner />}
    </div>
  );
}
