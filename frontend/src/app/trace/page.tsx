'use client';
import { Suspense, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import Link from 'next/link';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { SpanDetail } from '@/components/SpanDetail';
import { CopyButton } from '@/components/CopyButton';
import { CopilotExplain } from '@/components/CopilotExplain';
import { api } from '@/lib/api';
import { fmtNs, tsLong } from '@/lib/utils';
import type { SpanRow, TimeRange } from '@/lib/types';

function TraceDetailInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '1h' });
  const [spans, setSpans] = useState<SpanRow[] | null | undefined>(undefined);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    setSpans(undefined);
    api.trace(id).then(d => setSpans(d.spans ?? [])).catch(() => setSpans(null));
  }, [id]);

  if (!id) {
    return (
      <>
        <Topbar title="Trace" range={range} onRangeChange={setRange} />
        <div id="content"><Empty icon="⚠" title="Missing trace id" /></div>
      </>
    );
  }

  const sel = spans?.find(s => s.spanId === selectedId) ?? null;
  const root = spans?.find(s => !s.parentSpanId) ?? spans?.[0];
  const minT = spans && spans.length ? Math.min(...spans.map(s => s.startTime)) : 0;
  const maxT = spans && spans.length ? Math.max(...spans.map(s => s.endTime)) : 0;
  const totalNs = maxT - minT;
  const hasErr = spans?.some(s => s.statusCode === 'error') ?? false;


  return (
    <>
      <Topbar title="Trace Detail" range={range} onRangeChange={setRange} />
      <div id="content">
        <div style={{ marginBottom: 10, display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <button className="sec" onClick={() => router.back()}>← Back</button>
          <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
            {id}<CopyButton value={id} title="Copy trace ID" />
          </code>
          {spans && spans.length > 0 && (
            <>
              <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
              <span style={{ color: 'var(--text2)', fontSize: 12 }}>{spans.length} spans · {fmtNs(totalNs)}</span>
              {root && <span style={{ color: 'var(--text3)', fontSize: 12 }}>{tsLong(root.startTime)}</span>}
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
                <button className="sec"
                  onClick={() => exportTraceJSON(id, spans)}
                  title="Download this trace as JSON (full span list with attributes + events)"
                  style={{ fontSize: 12, padding: '3px 10px' }}>
                  ⬇ Export JSON
                </button>
                <Link href={`/logs?traceId=${id}`}
                  style={{ fontSize: 12, padding: '3px 10px',
                    background: 'var(--bg3)', border: '1px solid var(--border)',
                    borderRadius: 4, color: 'var(--accent2)', textDecoration: 'none' }}>
                  ≡ View logs
                </Link>
              </span>
            </>
          )}
        </div>

        {spans === undefined && <Spinner />}
        {spans && spans.length === 0 && <Empty icon="⋮" title="Trace not found" />}
        {spans && spans.length > 0 && (
          <>
            <div style={{ marginBottom: 10 }}>
              <CopilotExplain kind="trace" id={id} label="🤖 Explain this trace" />
            </div>
            <div id="td-outer">
              <div id="td-wf">
                <TraceWaterfall spans={spans} selectedId={selectedId} onSelect={setSelectedId} />
              </div>
              {sel && <SpanDetail span={sel} onClose={() => setSelectedId(null)} />}
            </div>
          </>
        )}
      </div>
    </>
  );
}

export default function TraceDetailPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <TraceDetailInner />
    </Suspense>
  );
}

// exportTraceJSON triggers a browser download of the full trace as a
// pretty-printed JSON file. Filename includes a short trace-id prefix
// so a folder of exports stays scannable. Pure client-side — no
// extra round-trip; the spans are already loaded.
function exportTraceJSON(traceId: string, spans: unknown[]) {
  const payload = JSON.stringify({ traceId, spans }, null, 2);
  const blob = new Blob([payload], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `trace-${traceId.slice(0, 12)}.json`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
