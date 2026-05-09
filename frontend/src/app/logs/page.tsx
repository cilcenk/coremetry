'use client';
import { Suspense, useEffect, useState } from 'react';
import Link from 'next/link';
import { useSearchParams } from 'next/navigation';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { CopyButton } from '@/components/CopyButton';
import { Pager } from '@/components/Pager';
import { api } from '@/lib/api';
import { tsShort, timeRangeToNs, sevName, sevClass } from '@/lib/utils';
import type { LogsResponse, LogRow, TimeRange } from '@/lib/types';

const SEV_OPTIONS = [
  { v: 0, label: 'All severities' },
  { v: 5, label: 'DEBUG+' },
  { v: 9, label: 'INFO+' },
  { v: 13, label: 'WARN+' },
  { v: 17, label: 'ERROR+' },
  { v: 21, label: 'FATAL' },
];

function LogsInner() {
  const searchParams = useSearchParams();

  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [page, setPage] = useState(0);
  const [filter, setFilter] = useState({
    service: '', search: '', severity: 0, traceId: '', spanId: '',
  });
  const [draft, setDraft] = useState(filter);
  const [data, setData] = useState<LogsResponse | null | undefined>(undefined);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [services, setServices] = useState<string[]>([]);
  // Live tail (HyperDX-style): poll every 2s, prepend new rows.
  const [live, setLive] = useState(false);

  // Sync filter state from URL params on every URL change. This
  // covers two cases that useState's lazy init does not handle
  // reliably: (a) static-prerender → CSR hydration, where useState
  // initializes against empty searchParams during SSG and never
  // re-runs even though the client sees real params; (b) in-app
  // navigations that update the URL without remounting the page.
  // Anomaly + service drill-down links rely on this — they pass
  // ?service=<svc>&q=<token> and expect the page to land already
  // scoped instead of showing the global all-logs view.
  useEffect(() => {
    const next = {
      service:  searchParams.get('service') ?? '',
      search:   searchParams.get('q') ?? searchParams.get('search') ?? '',
      severity: 0,
      traceId:  searchParams.get('traceId') ?? '',
      spanId:   searchParams.get('spanId')  ?? '',
    };
    setFilter(next);
    setDraft(next);
    setPage(0);
  }, [searchParams]);

  useEffect(() => {
    api.services(timeRangeToNs(range))
      .then(svcs => setServices((svcs ?? []).map(s => s.name)))
      .catch(() => setServices([]));
  }, [range]);

  useEffect(() => {
    setData(undefined); setExpanded(new Set());
    // When pinned to a trace, ignore the time range — old traces should
    // still surface their logs even if outside the current window.
    const useTimeRange = !filter.traceId;
    const { from, to } = useTimeRange ? timeRangeToNs(range) : { from: undefined, to: undefined };
    api.logs({
      limit: 100, offset: page * 100, from, to,
      service: filter.service || undefined,
      search: filter.search || undefined,
      severity: filter.severity > 0 ? filter.severity : undefined,
      traceId: filter.traceId || undefined,
      spanId:  filter.spanId  || undefined,
    })
      .then(setData).catch(() => setData(null));
  }, [range, filter, page]);

  // ── Live tail loop ───────────────────────────────────────────────────────
  useEffect(() => {
    if (!live) return;
    const fetchTail = () => {
      const now = Date.now() * 1_000_000;
      const from = Math.floor((Date.now() - 5 * 60_000) * 1_000_000); // last 5 min
      api.logs({
        limit: 200, from, to: now,
        service: filter.service || undefined,
        search: filter.search || undefined,
        severity: filter.severity > 0 ? filter.severity : undefined,
      }).then(r => setData(r)).catch(() => {});
    };
    fetchTail();
    const t = setInterval(fetchTail, 2000);
    return () => clearInterval(t);
  }, [live, filter.service, filter.search, filter.severity]);

  const apply = () => { setPage(0); setFilter(draft); };
  const reset = () => {
    const empty = { service: '', search: '', severity: 0, traceId: '', spanId: '' };
    setDraft(empty); setFilter(empty); setPage(0);
  };
  const clearTraceLock = () => {
    const next = { ...filter, traceId: '', spanId: '' };
    setFilter(next); setDraft(d => ({ ...d, traceId: '', spanId: '' }));
  };
  const toggle = (id: number) => {
    const next = new Set(expanded);
    next.has(id) ? next.delete(id) : next.add(id);
    setExpanded(next);
  };

  const logs = data?.logs ?? [];
  const total = data?.total ?? 0;

  return (
    <>
      <Topbar title="Logs" range={range} onRangeChange={setRange} />
      <div id="content">
        <SavedViewsBar page="logs" />
        {filter.traceId && (
          <div className="trace-lock">
            <span>Filtered to trace</span>
            <code>{filter.traceId}</code>
            {filter.spanId && (<>
              <span>· span</span>
              <code>{filter.spanId}</code>
            </>)}
            <button className="sec" onClick={clearTraceLock}>✕ Clear</button>
          </div>
        )}
        <div className="controls">
          <ServicePicker value={draft.service} onChange={v => setDraft({ ...draft, service: v })}
            placeholder="Service…" width={170} onEnter={apply} />
          <input
            placeholder='Search… (try `level:error`, `service.name:java-demo AND timeout`)'
            value={draft.search}
            onChange={e => setDraft({ ...draft, search: e.target.value })}
            onKeyDown={e => e.key === 'Enter' && apply()}
            title='Free-text on body. Lucene syntax supported when the logs backend is Elasticsearch — e.g. field:value, AND, OR, NOT, "phrase", value*. Plain words match the body.'
            style={{ width: 320 }} />
          <select value={draft.severity} onChange={e => setDraft({ ...draft, severity: Number(e.target.value) })}>
            {SEV_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.label}</option>)}
          </select>
          <button onClick={apply}>Search</button>
          <button className="sec" onClick={reset}>Reset</button>
          <button className={live ? 'live-on' : 'sec'}
            onClick={() => setLive(v => !v)}
            style={{ marginLeft: 'auto' }}
            title="Auto-refresh every 2 seconds with the latest logs">
            {live ? '⏸ Pause Live' : '▶ Live tail'}
          </button>
        </div>

        {data === undefined && <Spinner />}
        {data && logs.length === 0 && (
          filter.traceId ? (
            <Empty icon="≡" title="No logs match this trace">
              The trace exists in Coremetry, but the logs backend has no
              record of it. Two common reasons:
              <ul style={{ marginTop: 8, paddingLeft: 18, lineHeight: 1.6 }}>
                <li>The application emitted no log lines while this trace was active.</li>
                <li>The log shipper (Filebeat / OTel Collector ES exporter / etc.) hadn't started yet when the trace ran, so the log was never indexed.</li>
              </ul>
              {filter.spanId && <>You also filtered by span — try <a href="#" onClick={e => { e.preventDefault(); const next = { ...filter, spanId: '' }; setFilter(next); setDraft(d => ({ ...d, spanId: '' })); }}>removing the span filter</a> to see all logs for the trace.</>}
            </Empty>
          ) : (
            <Empty icon="≡" title="No logs found" />
          )
        )}
        {data && logs.length > 0 && (
          <>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Time</th><th>Sev</th><th>Service</th><th>Message</th><th>Trace</th>
                  </tr>
                </thead>
                <tbody>
                  {logs.map(l => (
                    <LogRowR key={l.id} l={l} expanded={expanded.has(l.id)} onClick={() => toggle(l.id)} />
                  ))}
                </tbody>
              </table>
            </div>
            <Pager page={page} pageSize={100} total={total} onPage={setPage}
                   extras={<>{total.toLocaleString()} total</>} />
          </>
        )}
      </div>
    </>
  );
}

function LogRowR({ l, expanded, onClick }: { l: LogRow; expanded: boolean; onClick: () => void }) {
  const attrs = Object.entries(l.attributes ?? {});
  const res = Object.entries(l.resourceAttributes ?? {});
  return (
    <>
      <tr onClick={onClick}>
        <td className="mono">{tsShort(l.timestamp)}</td>
        <td><span className={sevClass(l.severity)}>{l.severityText || sevName(l.severity)}</span></td>
        <td>
          <span style={{ fontSize: 11, padding: '1px 6px', background: 'var(--bg3)', borderRadius: 3, fontFamily: 'monospace' }}>
            {l.serviceName}
          </span>
        </td>
        <td style={{ maxWidth: 480 }} title={l.body}>{l.body}</td>
        <td className="mono">
          {l.traceId ? (
            <>
              <Link href={`/trace?id=${l.traceId}`} onClick={e => e.stopPropagation()}>
                {l.traceId.slice(0, 8)}…
              </Link>
              <CopyButton value={l.traceId} title="Copy trace ID" />
            </>
          ) : '—'}
        </td>
      </tr>
      {expanded && (
        <tr>
          <td colSpan={5} style={{ background: 'var(--bg0)', padding: '10px 20px' }}>
            <pre style={{ fontSize: 12, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', marginBottom: attrs.length ? 8 : 0 }}>
              {l.body}
            </pre>
            {attrs.length > 0 && (
              <table className="kv-table"><tbody>
                {attrs.map(([k, v]) => <tr key={k}><td>{k}</td><td>{String(v)}</td></tr>)}
              </tbody></table>
            )}
            {res.length > 0 && (
              <details style={{ marginTop: 6 }}>
                <summary style={{ cursor: 'pointer', fontSize: 11, color: 'var(--text2)' }}>Resource ({res.length})</summary>
                <table className="kv-table"><tbody>
                  {res.map(([k, v]) => <tr key={k}><td>{k}</td><td>{String(v)}</td></tr>)}
                </tbody></table>
              </details>
            )}
          </td>
        </tr>
      )}
    </>
  );
}

export default function LogsPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <LogsInner />
    </Suspense>
  );
}
