import { Suspense, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Topbar } from '@/components/Topbar';
import { SavedViewsBar } from '@/components/SavedViewsBar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { Combobox } from '@/components/Combobox';
import { ServicePicker } from '@/components/ServicePicker';
import { CopyButton } from '@/components/CopyButton';
import { LogTable } from '@/components/LogTable';
import { Pager } from '@/components/Pager';
import { useLogs } from '@/lib/queries';
import { useTableNav } from '@/lib/useTableNav';
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
  const [searchParams] = useSearchParams();

  const [range, setRange] = useState<TimeRange>({ preset: '15m' });
  const [page, setPage] = useState(0);
  const [filter, setFilter] = useState({
    service: '', search: '', severity: 0, traceId: '', spanId: '',
  });
  const [draft, setDraft] = useState(filter);
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

  // Build the params for the static-window query. When live
  // tail is on, we don't run this query (the live useQuery
  // below takes over instead) — `enabled: !live` gates it.
  //
  // CRITICAL: `from` / `to` are computed via timeRangeToNs which
  // reads Date.now() for non-custom presets. Without memoising,
  // every render produces a NEW from/to (Date.now() advanced by
  // a few ms), the React Query key hashes differently, RQ
  // starts a fresh query and discards the previous — isLoading
  // stays true forever and the page is stuck on the skeleton.
  // Memoise on the range / traceId-filter so the values only
  // refresh when the operator actually changes the inputs.
  const useTimeRange = !filter.traceId;
  const { from, to } = useMemo(
    () => useTimeRange ? timeRangeToNs(range) : { from: undefined, to: undefined },
    [useTimeRange, range],
  );
  const staticQ = useLogs({
    limit: 100, offset: page * 100, from, to,
    service: filter.service || undefined,
    search: filter.search || undefined,
    severity: filter.severity > 0 ? filter.severity : undefined,
    traceId: filter.traceId || undefined,
    spanId:  filter.spanId  || undefined,
  });

  // Live-tail query — separate hook with refetchInterval so
  // RQ owns the polling loop. The query key includes the
  // service/search/severity filters but NOT a moving `from`/
  // `to` (the polling fetches the latest 5 min each tick).
  // Disabled when live=false; the static query (above) takes
  // over in that case.
  const liveQ = useQuery({
    queryKey: ['logs', 'live', filter.service, filter.search, filter.severity],
    queryFn: () => {
      const now = Date.now() * 1_000_000;
      const fromNs = Math.floor((Date.now() - 5 * 60_000) * 1_000_000);
      return api.logs({
        limit: 200, from: fromNs, to: now,
        service: filter.service || undefined,
        search: filter.search || undefined,
        severity: filter.severity > 0 ? filter.severity : undefined,
      });
    },
    enabled: live,
    refetchInterval: live ? 2_000 : false,
    staleTime: 0,
  });

  // Merge: when live is on, prefer the live data; otherwise
  // the static window result. Mirrors the previous setData
  // behaviour where live overwrote static.
  const dataSource = live ? liveQ : staticQ;
  const data = dataSource.isLoading ? undefined
    : dataSource.isError ? null
    : dataSource.data;

  // Reset expansion state when the filter / range / page
  // changes — opening row #5 in one window doesn't translate
  // to the next.
  useEffect(() => { setExpanded(new Set()); }, [range, filter, page]);

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

  // j/k row navigation — same pattern as /services and /traces.
  // Enter / o on the selected row toggles the expansion
  // (matches the existing click behaviour), Esc clears the
  // selection. The hook scrolls the active row into view via
  // [data-row-idx], which we set on the LogRowR below.
  const tableNav = useTableNav<LogRow>(logs, {
    onOpen: (l) => toggle(l.id),
    pageId: 'logs',
  });

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
            placeholder='Search… (KQL: level:error AND service.name:"checkout")'
            value={draft.search}
            onChange={e => setDraft({ ...draft, search: e.target.value })}
            onKeyDown={e => e.key === 'Enter' && apply()}
            title={'Free-text on body OR KQL/Lucene syntax (Elasticsearch backend).\n\n' +
              'Examples:\n' +
              '  level:error\n' +
              '  service.name:"checkout-svc" AND NOT message:health\n' +
              '  trace.id:c9ea*\n' +
              '  message:"connection refused" AND k8s.namespace:prod\n\n' +
              'Plain words match the body. Use double quotes for exact phrases.'}
            style={{ width: 380 }} />
          {/* Trace ID filter — dedicated input next to search so
              operators can paste a trace ID from a problem /
              incident and see only its log lines. Mirrors the
              ?traceId= URL param the deep-link routes already
              use. The backend filter is an exact term match
              against trace.id (ES) / trace_id (CH). Trimmed +
              lowercased so a paste of `0xABC…` or whitespace
              padding still works. */}
          <input
            placeholder="Trace ID"
            value={draft.traceId}
            onChange={e => setDraft({ ...draft, traceId: e.target.value.trim().toLowerCase().replace(/^0x/, '') })}
            onKeyDown={e => e.key === 'Enter' && apply()}
            title="Filter logs to a single trace. Time range is ignored when this is set — searches across full retention."
            className="mono"
            style={{ width: 180, fontSize: 12 }} />
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

        {data === undefined && <TableSkeleton rows={12} cols={5} />}
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
            <LogTable logs={logs} nav={tableNav}
              expandedIds={expanded}
              onToggleExpand={toggle} />
            <Pager page={page} pageSize={100} total={total} onPage={setPage}
                   extras={<>{total.toLocaleString()} total</>} />
          </>
        )}
      </div>
    </>
  );
}

// LogRowR moved to components/LogTable.tsx (shared between
// /logs and the trace detail Logs tab).

export default function LogsPage() {
  return (
    <Suspense fallback={<Spinner />}>
      <LogsInner />
    </Suspense>
  );
}
