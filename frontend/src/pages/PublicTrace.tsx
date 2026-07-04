import { Suspense, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Spinner, Empty } from '@/components/Spinner';
import { Wordmark } from '@/components/Wordmark';
import { TraceWaterfall } from '@/components/TraceWaterfall';
import { SpanDetail } from '@/components/SpanDetail';
import { TelescopeIcon } from '@/components/TelescopeIcon';
import { LogTable } from '@/components/LogTable';
import { fmtNs, tsLong } from '@/lib/utils';
import type { SpanRow, LogRow } from '@/lib/types';

// Public read-only trace viewer. Hit by /public/trace?token=xxx;
// the URL encoding lets the snapshot link be plain HTTP-shareable
// (email, Slack, Jira, etc.) without Coremetry auth.
//
// Behaviour mirrors /trace?id=… but stripped down: no Topbar, no
// auth-gated controls (Edit / Share again / Copilot Explain), no
// time range picker. The waterfall + SpanDetail components are
// reused as-is — they were already presentation-only.

// v0.8.252 — snapshot logs: frozen at share time by the backend, so
// the public viewer shows exactly what the sharer saw ("o andaki"
// loglar) without ever querying the live logstore. The backend
// serializes full logstore.LogRecord rows, which is exactly the
// LogRow wire shape — so the shared <LogTable> renders them
// unchanged and the public Logs tab matches the logged-in one.
interface PublicTraceResponse {
  traceId: string;
  spans: SpanRow[];
  logs?: LogRow[];
  expiresAt: number;
  createdBy?: string;
}

function PublicTraceInner() {
  const [sp] = useSearchParams();
  const token = sp.get('token') ?? '';
  const [data, setData] = useState<PublicTraceResponse | null | undefined>(undefined);
  const [selectedId, setSelectedId] = useState<string | null>(sp.get('span'));
  const [tab, setTab] = useState<'waterfall' | 'logs'>('waterfall');

  useEffect(() => {
    if (!token) return;
    fetch(`/api/public/trace/${token}`)
      .then(async r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.json() as Promise<PublicTraceResponse>;
      })
      .then(setData)
      .catch(() => setData(null));
  }, [token]);

  if (!token) {
    return <Empty icon="⚠" title="Missing share token" />;
  }
  if (data === undefined) return <Spinner />;
  if (data === null) {
    return <Empty icon="⚠" title="Snapshot not found or expired">
      The link you opened is no longer valid. Ask whoever shared it to mint a fresh one.
    </Empty>;
  }

  // Empty span list is possible (snapshot of an in-progress trace
  // whose root hasn't been flushed, or a trace with all spans
  // dropped by sampling). Without the null guard, `root.startTime`
  // below crashes the whole public viewer with a white screen
  // instead of showing the "no spans" empty state.
  if (!data.spans || data.spans.length === 0) {
    return <Empty icon="⚠" title="Snapshot has no spans">
      The trace was captured but no spans are attached — the link is valid
      but there's nothing to render.
    </Empty>;
  }
  const sel = data.spans.find(s => s.spanId === selectedId) ?? null;
  const root = data.spans.find(s => !s.parentSpanId) ?? data.spans[0];
  const minT = Math.min(...data.spans.map(s => s.startTime));
  const maxT = Math.max(...data.spans.map(s => s.endTime));
  const totalNs = maxT - minT;
  const hasErr = data.spans.some(s => s.statusCode === 'error');
  const logs = data.logs ?? [];

  return (
    <div style={{ minHeight: '100vh', background: 'var(--bg)', padding: '20px 24px' }}>
      {/* Public page header — minimal product chrome (logo + name)
          plus the share metadata (who minted it + when it expires)
          so the recipient knows what they're looking at. */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12, marginBottom: 18,
        paddingBottom: 14, borderBottom: '1px solid var(--border)',
      }}>
        <TelescopeIcon size={28} />
        <div>
          <div style={{ fontSize: 15, fontWeight: 700, letterSpacing: '0.3px' }}><Wordmark /></div>
          <div style={{ fontSize: 11, color: 'var(--text3)' }}>
            Shared trace · public read-only snapshot
          </div>
        </div>
        <div style={{ flex: 1 }} />
        <div style={{ fontSize: 11, color: 'var(--text3)', textAlign: 'right', lineHeight: 1.5 }}>
          {data.createdBy && <>Shared by <code style={{ background: 'var(--bg2)', padding: '1px 5px', borderRadius: 3 }}>{data.createdBy}</code><br /></>}
          Expires {tsLong(data.expiresAt)}
        </div>
      </div>

      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14, flexWrap: 'wrap' }}>
        <code style={{ fontSize: 11, color: 'var(--text2)', background: 'var(--bg2)', padding: '2px 6px', borderRadius: 4 }}>
          {data.traceId}
        </code>
        {data.spans.length > 0 && (
          <>
            <span className={`badge ${hasErr ? 'b-err' : 'b-ok'}`}>{hasErr ? 'ERROR' : 'OK'}</span>
            <span style={{ color: 'var(--text2)', fontSize: 12 }}>{data.spans.length} spans · {fmtNs(totalNs)}</span>
            {root && <span style={{ color: 'var(--text3)', fontSize: 12 }}>{tsLong(root.startTime)}</span>}
          </>
        )}
      </div>

      {/* Waterfall | Logs — same .tab-strip pattern as the logged-in
          trace page so the recipient sees the exact same anatomy.
          The Logs tab renders the SNAPSHOT frozen at share time; it
          never queries the live logstore from this anonymous route. */}
      <div className="tab-strip" style={{ marginBottom: 10 }}>
        <button className={tab === 'waterfall' ? 'active' : ''} onClick={() => setTab('waterfall')}>
          Waterfall <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{data.spans.length}</span>
        </button>
        <button className={tab === 'logs' ? 'active' : ''} onClick={() => setTab('logs')}>
          Logs <span style={{ color: 'var(--text3)', marginLeft: 4 }}>{logs.length}</span>
        </button>
      </div>

      {tab === 'waterfall' && (
        <div id="td-outer">
          <div id="td-wf">
            <TraceWaterfall spans={data.spans} selectedId={selectedId} onSelect={setSelectedId} />
          </div>
          {sel && <SpanDetail span={sel} onClose={() => setSelectedId(null)} />}
        </div>
      )}

      {tab === 'logs' && (
        logs.length === 0 ? (
          <Empty icon="≡" title="No logs in this snapshot">
            Logs are frozen into the share when the link is minted. This share
            was created before log capture existed, or the trace had no
            correlated log lines at share time.
          </Empty>
        ) : (
          <>
            <div style={{
              display: 'flex', gap: 10, padding: '6px 10px',
              fontSize: 11, color: 'var(--text3)',
            }}>
              <span>{logs.length} log line{logs.length === 1 ? '' : 's'} · captured at share time</span>
            </div>
            <LogTable logs={[...logs].sort((a, b) => a.timestamp - b.timestamp)} hideTraceColumn />
          </>
        )
      )}
    </div>
  );
}

export default function PublicTracePage() {
  return (
    <Suspense fallback={<Spinner />}>
      <PublicTraceInner />
    </Suspense>
  );
}
