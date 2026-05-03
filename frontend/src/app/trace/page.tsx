'use client';
import { Suspense, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import Link from 'next/link';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { SpanDetail } from '@/components/SpanDetail';
import { CopyButton } from '@/components/CopyButton';
import { api } from '@/lib/api';
import { fmtNs, tsLong } from '@/lib/utils';
import type { SpanRow, TimeRange } from '@/lib/types';

function TraceDetailInner() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const id = searchParams.get('id') ?? '';

  const [range, setRange] = useState<TimeRange>({ preset: '24h' });
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
              <Link href={`/logs?traceId=${id}`}
                style={{ marginLeft: 'auto', fontSize: 12, padding: '3px 10px',
                  background: 'var(--bg3)', border: '1px solid var(--border)',
                  borderRadius: 4, color: 'var(--accent2)', textDecoration: 'none' }}>
                ≡ View logs
              </Link>
            </>
          )}
        </div>

        {spans === undefined && <Spinner />}
        {spans && spans.length === 0 && <Empty icon="⋮" title="Trace not found" />}
        {spans && spans.length > 0 && (
          <div id="td-outer">
            <div id="td-wf">
              <TraceWaterfall spans={spans} selectedId={selectedId} onSelect={setSelectedId} />
            </div>
            {sel && <SpanDetail span={sel} onClose={() => setSelectedId(null)} />}
          </div>
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
