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
import { LogsFacetSidebar } from '@/components/LogsFacetSidebar';
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

  // Field-mapping hint (v0.5.136 / v0.5.137). On the Elastic
  // backend, surface what fields are queryable so the operator
  // doesn't have to guess what their index mapping exposes. CH
  // backend returns an empty list (its shape is fixed and
  // already documented in the placeholder).
  const [fields, setFields] = useState<string[]>([]);
  const [showFieldsHint, setShowFieldsHint] = useState(false);
  useEffect(() => {
    api.logsFields()
      .then(d => setFields(d.fields ?? []))
      .catch(() => setFields([]));
  }, []);
  // Insert "field:" into the search box at cursor / end. Auto-
  // focuses so the operator can type the value immediately.
  const insertField = (f: string) => {
    const cur = draft.search;
    const sep = cur && !cur.endsWith(' ') ? ' AND ' : '';
    setDraft({ ...draft, search: `${cur}${sep}${f}:` });
    // Move keyboard focus back to the input.
    requestAnimationFrame(() => {
      const el = document.querySelector<HTMLInputElement>('input[placeholder^="Search…"]');
      el?.focus();
      el?.setSelectionRange(el.value.length, el.value.length);
    });
  };

  const apply = () => { setPage(0); setFilter(draft); };
  const reset = () => {
    const empty = { service: '', search: '', severity: 0, traceId: '', spanId: '' };
    setDraft(empty); setFilter(empty); setPage(0);
  };

  // Facet sidebar click handler (v0.5.226). Service buckets set
  // the service picker directly; the rest fold into the search
  // box as `key:value` KQL clauses. Clicking an already-active
  // value clears it instead of re-appending. Auto-applies — the
  // sidebar is a click-to-filter affordance, not a draft input.
  const applyFacet = (field: import('@/components/LogsFacetSidebar').FacetField, value: string) => {
    if (field === 'service') {
      const next = filter.service === value
        ? { ...filter, service: '' }
        : { ...filter, service: value };
      setDraft(d => ({ ...d, service: next.service }));
      setFilter(next);
      setPage(0);
      return;
    }
    // Map to KQL key. severity → level, pod → kubernetes.pod_name, etc.
    const map: Record<string, string> = {
      severity:  'level',
      pod:       'kubernetes.pod_name',
      container: 'kubernetes.container_name',
      cluster:   'openshift.labels.cluster',
    };
    const k = map[field];
    if (!k) return;
    // Toggle: if the search already contains `key:value` (with
    // optional quotes) strip it; otherwise append with AND glue.
    const re = new RegExp(`(?:^|\\s+AND\\s+|\\s+)${k.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\$&')}:"?${value.replace(/[.*+?^${}()|[\\]\\\\]/g, '\\$&')}"?`);
    let nextSearch: string;
    if (re.test(filter.search)) {
      nextSearch = filter.search.replace(re, ' ').replace(/\s+AND\s+AND\s+/g, ' AND ').trim()
        .replace(/^AND\s+/, '').replace(/\s+AND$/, '');
    } else {
      const sep = filter.search ? ' AND ' : '';
      // Quote the value when it has whitespace; otherwise plain.
      const v = /\s/.test(value) ? `"${value}"` : value;
      nextSearch = `${filter.search}${sep}${k}:${v}`;
    }
    const next = { ...filter, search: nextSearch };
    setDraft(d => ({ ...d, search: nextSearch }));
    setFilter(next);
    setPage(0);
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
      <div id="content" style={{ display: 'flex', alignItems: 'flex-start' }}>
        <LogsFacetSidebar range={range} filter={filter} onApplyValue={applyFacet} />
        <div style={{ flex: 1, minWidth: 0 }}>
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
          {fields.length > 0 && (
            <button type="button" className="sec"
              onClick={() => setShowFieldsHint(v => !v)}
              title={`${fields.length} indexable field${fields.length === 1 ? '' : 's'} discovered from the Elasticsearch mapping. Click to browse + auto-insert into the search.`}
              style={{ fontSize: 11, padding: '3px 8px' }}>
              {showFieldsHint ? '× Fields' : 'ƒ Fields'}
            </button>
          )}
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

        {/* Field-mapping chips (v0.5.137). Toggled via the ƒ
            Fields button next to the search input. Discovered
            from the Elasticsearch _mapping; clicking a chip
            inserts "field:" at the end of the current search
            with the right AND glue + auto-focuses the input. */}
        {showFieldsHint && fields.length > 0 && (
          <div style={{
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 6, padding: 8, marginBottom: 10,
            fontSize: 11,
          }}>
            <div style={{
              fontWeight: 600, color: 'var(--text2)', marginBottom: 6,
              display: 'flex', alignItems: 'baseline', gap: 8,
            }}>
              <span>Discovered fields</span>
              <span style={{ color: 'var(--text3)', fontWeight: 400 }}>
                {fields.length} field{fields.length === 1 ? '' : 's'} · click to insert "field:" into the search
              </span>
            </div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
              {fields.map(f => (
                <button key={f} type="button"
                  onClick={() => insertField(f)}
                  className="sec"
                  style={{
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                    fontSize: 11, padding: '2px 6px',
                  }}>
                  {f}
                </button>
              ))}
            </div>
          </div>
        )}

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
              onToggleExpand={toggle}
              extraExpanded={l => <SimilarTracesPanel body={l.body} />} />
            <Pager page={page} pageSize={100} total={total} onPage={setPage}
                   extras={<>{total.toLocaleString()} total</>} />
          </>
        )}
        </div>
      </div>
    </>
  );
}

// SimilarTracesPanel (v0.5.141) — collapsed-by-default block in
// the expanded log row. Click "Find similar" → POSTs the body to
// /api/logs/similar (Elastic more_like_this + trace.id terms
// agg) and renders the bucketed trace IDs as clickable links.
// CH-backend deployments see a tooltip explaining ES is needed
// rather than a noisy error; ES installs get a one-click pivot
// to "all traces that produced a log like this one".
function SimilarTracesPanel({ body }: { body: string }) {
  const [state, setState] = useState<
    | { kind: 'idle' }
    | { kind: 'loading' }
    | { kind: 'ok'; traces: Array<{ traceId: string; count: number }> }
    | { kind: 'err'; msg: string }
  >({ kind: 'idle' });
  const run = async () => {
    setState({ kind: 'loading' });
    try {
      const r = await api.logsSimilarTraces(body, 50);
      setState({ kind: 'ok', traces: r.traces ?? [] });
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'similar-logs lookup failed';
      setState({ kind: 'err', msg });
    }
  };
  return (
    <div style={{
      marginTop: 8, paddingTop: 8,
      borderTop: '1px dashed var(--border)',
      fontSize: 11,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <button type="button" className="sec"
          onClick={run}
          disabled={state.kind === 'loading'}
          style={{ fontSize: 11, padding: '3px 10px' }}
          title="Find traces whose logs contain text similar to this one (more_like_this on the body field; Elasticsearch backend only).">
          {state.kind === 'loading' ? 'Searching…' : '⌕ Find similar traces'}
        </button>
        {state.kind === 'ok' && (
          <span style={{ color: 'var(--text3)' }}>
            {state.traces.length} trace{state.traces.length === 1 ? '' : 's'} match
          </span>
        )}
        {state.kind === 'err' && (
          <span style={{ color: 'var(--err)' }}>{state.msg}</span>
        )}
      </div>
      {state.kind === 'ok' && state.traces.length > 0 && (
        <div style={{ marginTop: 6, display: 'flex', flexWrap: 'wrap', gap: 4 }}>
          {state.traces.map(t => (
            <Link key={t.traceId}
              to={`/trace/${encodeURIComponent(t.traceId)}`}
              title={`${t.count} similar log line${t.count === 1 ? '' : 's'} in this trace`}
              style={{
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: 10, padding: '2px 6px', borderRadius: 3,
                background: 'var(--bg2)', border: '1px solid var(--border)',
                color: 'var(--text)', textDecoration: 'none',
              }}>
              {t.traceId.slice(0, 16)}<span style={{ color: 'var(--text3)' }}> · {t.count}</span>
            </Link>
          ))}
        </div>
      )}
    </div>
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
