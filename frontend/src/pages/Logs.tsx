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
import { TracePeekDrawer } from '@/components/TracePeekDrawer';
import { LogsHistogram } from '@/components/LogsHistogram';
import { LogPatternStrip } from '@/components/LogPatternStrip';
import { LivePatternsPanel } from '@/components/LivePatternsPanel';
import { LogTemplatesPanel } from '@/components/LogTemplatesPanel';
import { Pager } from '@/components/Pager';
import { buildKibanaURL } from '@/lib/kibanaLink';
import type { KibanaSettings } from '@/lib/types';
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

// Compose a KQL clause from the current filter state so the
// Kibana deep-link lands on the same slice. service / trace_id
// become per-field clauses; the free-text search string passes
// through verbatim (it's already KQL on this page). Returned
// string may be empty — Kibana handles "no query" cleanly.
function buildKQLFromFilter(f: {
  service: string; search: string; severity: number; traceId: string; spanId: string;
}): string {
  const parts: string[] = [];
  if (f.service) parts.push(`service.name:"${f.service.replace(/"/g, '\\"')}"`);
  if (f.traceId) parts.push(`trace.id:"${f.traceId}"`);
  if (f.spanId)  parts.push(`span.id:"${f.spanId}"`);
  if (f.severity > 0) {
    // Map OTel severity number to a level name range; Kibana's
    // log.level field typically holds the canonical text.
    const min = f.severity;
    if (min >= 21) parts.push('log.level:"FATAL"');
    else if (min >= 17) parts.push('log.level:("FATAL" OR "ERROR")');
    else if (min >= 13) parts.push('log.level:("FATAL" OR "ERROR" OR "WARN")');
  }
  if (f.search.trim()) parts.push(f.search.trim());
  return parts.join(' AND ');
}

function LogsInner() {
  const [searchParams] = useSearchParams();

  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [page, setPage] = useState(0);
  const [filter, setFilter] = useState({
    service: '', search: '', severity: 0, traceId: '', spanId: '',
  });
  const [draft, setDraft] = useState(filter);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [services, setServices] = useState<string[]>([]);
  // v0.5.399 — trace peek drawer state. Clicking the "👁" button
  // next to a trace_id in the log row sets this; TracePeekDrawer
  // fetches the trace summary + sibling logs and renders inline
  // without disturbing the page's existing filter/search state.
  const [peekTraceId, setPeekTraceId] = useState<string | null>(null);
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
  // Save-as-alert modal state (v0.5.242). null = closed; an
  // object = open with this draft.
  const [alertDraft, setAlertDraft] = useState<{
    name: string;
    threshold: number;
    windowSec: number;
    severity: string;
  } | null>(null);
  const [alertSaving, setAlertSaving] = useState(false);
  const [alertMsg, setAlertMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  // Kibana deep-link config (v0.5.236). Loaded once on mount;
  // when disabled or unconfigured, buildKibanaURL returns null
  // and the button doesn't render.
  const [kibana, setKibana] = useState<KibanaSettings | null>(null);
  useEffect(() => {
    api.getKibanaSettings()
      .then(s => setKibana(s ?? null))
      .catch(() => setKibana(null));
  }, []);
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

  // Append/remove a `key:value` clause from the search box.
  // `negate=true` produces a `NOT key:value` clause. Used by both
  // the facet sidebar and the expanded-row KvRow click-to-filter
  // buttons. Auto-applies (commits filter + resets page).
  const toggleSearchClause = (key: string, value: string, negate = false) => {
    const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    // Always wrap values in double quotes — Lucene treats many
    // characters as operators (`-`, `/`, `:`, `*`, etc.) and a
    // bare hostname like "my-host-7f-abc" is parsed as a boolean
    // expression rather than a literal. Inside quotes only `\`
    // and `"` are special, which we escape. v0.5.230 caught a
    // `host.hostname:my-host-abc` filter never matching.
    const phraseQuote = (s: string) =>
      `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
    const k = escapeRe(key);
    const v = escapeRe(value);
    // Match the existing clause (with or without NOT, with or
    // without surrounding quotes). One regex finds both
    // positive + negative forms so an exact ⊕ → ⊖ toggle works
    // without duplicate clauses piling up.
    const re = new RegExp(`(?:^|\\s+AND\\s+|\\s+)(?:NOT\\s+)?${k}:"?${v}"?`);
    let nextSearch: string;
    if (re.test(filter.search)) {
      const alreadyNegated = new RegExp(`(?:^|\\s+AND\\s+|\\s+)NOT\\s+${k}:"?${v}"?`).test(filter.search);
      nextSearch = filter.search.replace(re, ' ')
        .replace(/\s+AND\s+AND\s+/g, ' AND ').trim()
        .replace(/^AND\s+/, '').replace(/\s+AND$/, '');
      if (alreadyNegated !== negate) {
        const sep = nextSearch ? ' AND ' : '';
        nextSearch = `${nextSearch}${sep}${negate ? 'NOT ' : ''}${key}:${phraseQuote(value)}`;
      }
    } else {
      const sep = filter.search ? ' AND ' : '';
      nextSearch = `${filter.search}${sep}${negate ? 'NOT ' : ''}${key}:${phraseQuote(value)}`;
    }
    const next = { ...filter, search: nextSearch };
    setDraft(d => ({ ...d, search: nextSearch }));
    setFilter(next);
    setPage(0);
  };

  // Expanded-row KvRow click-to-filter handlers (v0.5.229).
  // Operator clicks ⊕ on any attribute / resource attribute →
  // adds key:value to search. ⊖ → adds NOT key:value.
  const addFromRow      = (key: string, value: string) => toggleSearchClause(key, value, false);
  const excludeFromRow  = (key: string, value: string) => toggleSearchClause(key, value, true);
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
          {/* Create watcher (v0.5.252 — was: 🔔 Save as alert).
              Refactored to mirror Elasticsearch Watcher's mental
              model: Trigger / Condition / Action sections, so the
              operator reads the rule as a sentence ("Every 1m,
              count(<query>) over last 60s > 10 → page warning")
              instead of three flat inputs. Backend payload is
              unchanged — same saved-search alert rule introduced
              in v0.5.242. Emoji dropped at operator request:
              status-bar buttons should look professional, not
              messaging-app-ish. */}
          {draft.search.trim() !== '' && (
            <button className="sec"
              onClick={() => setAlertDraft({
                name: '', threshold: 10, windowSec: 60, severity: 'warning',
              })}
              title="Create a watcher — periodic poll of this query with a threshold + notification"
              style={{ fontSize: 12 }}>
              Create watcher
            </button>
          )}
          <button className={live ? 'live-on' : 'sec'}
            onClick={() => setLive(v => !v)}
            style={{ marginLeft: 'auto' }}
            title="Auto-refresh every 2 seconds with the latest logs">
            {live ? '⏸ Pause Live' : '▶ Live tail'}
          </button>
          {/* External Kibana deep-link (v0.5.236). Hidden unless
              the admin has filled in Settings → Kibana link.
              Carries the current filter context — service /
              trace-id / search clauses become KQL; window
              becomes the time range — so the operator lands on
              the same slice they're looking at here. */}
          {(() => {
            const kql = buildKQLFromFilter(filter);
            const href = buildKibanaURL(kibana, {
              fromNs: from ?? undefined,
              toNs: to ?? undefined,
              kql,
            });
            if (!href) return null;
            return (
              <a href={href} target="_blank" rel="noopener"
                className="sec"
                title="Open the current filter slice in Kibana Discover"
                style={{
                  fontSize: 12, padding: '5px 12px',
                  textDecoration: 'none', color: 'var(--accent2)',
                }}>
                ↗ Discover in Kibana
              </a>
            );
          })()}
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

        {/* Log anomaly strip (v0.5.239) — curated patterns
            (OOMKilled / panic / NPE / deadlock / TLS / etc.)
            that are NEW or 2×+ over baseline. Click a chip →
            narrow the table to that pattern + its firing
            service. Renders nothing when there's no signal. */}
        <LogPatternStrip onSelect={({ search: s, service: sv }) => {
          const next = {
            ...filter,
            search: s,
            service: sv || filter.service,
          };
          setDraft(d => ({ ...d, search: s, service: sv || d.service }));
          setFilter(next);
          setPage(0);
        }} />

        {/* Live patterns panel (v0.5.243) — ES significant_text
            unsupervised over (last 15min vs last 24h). Surfaces
            tokens that just got over-represented vs baseline:
            new exception types, rare error codes, unusual
            phrases. Click a chip → narrow search to that token.
            Hidden on CH backend (no native equivalent at
            billion-row scale). */}
        <LivePatternsPanel onSelect={token => {
          // Use the shorthand "body" key; the backend's
          // expandShorthand rewrites it to an OR across all
          // body-field candidates (message / Body / log.message)
          // so the filter works regardless of mapping.
          toggleSearchClause('body', token, false);
        }} />

        {/* Drain-extracted templates (v0.5.244) — persistent
            log-shape ledger. Default sort: first_seen desc so
            new shapes land first. Click a template → search
            box gets the two most distinctive tokens from that
            shape (Java class names + logger paths rank
            highest in the scorer). */}
        <LogTemplatesPanel onSelectTemplate={substring => {
          // Substring search is body-field bound; the backend
          // shorthand expander rewrites "body:" to the right
          // field for the configured ES mapping.
          const next = { ...filter, search: substring };
          setDraft(d => ({ ...d, search: substring }));
          setFilter(next);
          setPage(0);
        }} />

        {/* Severity-stacked histogram (v0.5.235) — spike of errors
            stands out against the background INFO traffic without
            reading the count column. Hidden when neither a time
            range nor a trace pin is set; renders nothing on
            empty data. */}
        <LogsHistogram range={{ from, to }} filter={filter} />

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
              onFilterAdd={addFromRow}
              onFilterExclude={excludeFromRow}
              onTracePeek={tid => setPeekTraceId(tid)}
              extraExpanded={l => <SimilarTracesPanel body={l.body} />} />
            <Pager page={page} pageSize={100} total={total} onPage={setPage}
                   extras={<>{total.toLocaleString()} total</>} />
          </>
        )}
      </div>
      <TracePeekDrawer traceId={peekTraceId} onClose={() => setPeekTraceId(null)} />
      {alertDraft && (
        <SaveAsAlertModal
          query={filter.search}
          draft={alertDraft}
          onChange={setAlertDraft}
          saving={alertSaving}
          msg={alertMsg}
          onClose={() => { setAlertDraft(null); setAlertMsg(null); }}
          onSave={async () => {
            if (!alertDraft) return;
            setAlertSaving(true); setAlertMsg(null);
            try {
              await api.createAlertRule({
                name: alertDraft.name || `Log alert: ${filter.search.slice(0, 40)}`,
                service: '',
                metric: 'log_query',
                comparator: '>',
                threshold: alertDraft.threshold,
                windowSec: alertDraft.windowSec,
                severity: alertDraft.severity,
                enabled: true,
                logQuery: filter.search,
              });
              setAlertMsg({ kind: 'ok', text: 'Saved — rule is now evaluating on every tick.' });
              setTimeout(() => { setAlertDraft(null); setAlertMsg(null); }, 1200);
            } catch (err) {
              setAlertMsg({ kind: 'err',
                text: err instanceof Error ? err.message : 'Save failed' });
            } finally {
              setAlertSaving(false);
            }
          }} />
      )}
    </>
  );
}

// SaveAsAlertModal — Watcher-style overlay (v0.5.252, was the flat
// "Save as alert" of v0.5.242). Mirrors Elasticsearch Watcher's
// three-pane shape: Trigger (cadence) / Input + Condition (the
// query + threshold + window) / Action (severity). The operator
// reads it as a sentence — "Every tick, when count(query) > N in
// last Ws, page <severity>". Cadence is currently fixed to the
// evaluator tick (1m default); surfacing it as a labelled section
// makes the model explicit + opens the door to per-watcher
// frequency overrides later without another UI refactor.
function SaveAsAlertModal({
  query, draft, onChange, saving, msg, onClose, onSave,
}: {
  query: string;
  draft: { name: string; threshold: number; windowSec: number; severity: string };
  onChange: (next: typeof draft) => void;
  saving: boolean;
  msg: { kind: 'ok' | 'err'; text: string } | null;
  onClose: () => void;
  onSave: () => void;
}) {
  const sevColor = draft.severity === 'critical' ? 'var(--err)'
    : draft.severity === 'warning' ? 'var(--warn, #f0b352)'
    : 'var(--text2)';
  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.55)',
      display: 'grid', placeItems: 'center', zIndex: 200,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 540, padding: 20, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        <div style={{ fontSize: 15, fontWeight: 700, marginBottom: 2 }}>
          New watcher
        </div>
        <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 14 }}>
          Polls the saved query on every evaluator tick, fires + fans out to
          notification channels when the threshold is breached. Same
          mechanism the alert evaluator uses for metric / span rules; logs
          ride the same fire-once-per-window de-dupe and severity routing.
        </div>

        {/* Sentence preview — the operator's mental model in one
            line, derived live from the inputs below. Datadog +
            Elastic Watcher both surface this; it removes the
            "wait what does this rule actually do" beat. */}
        <div style={{
          padding: '8px 10px', marginBottom: 14,
          background: 'var(--bg0)', border: '1px solid var(--border)',
          borderRadius: 4, fontSize: 12, lineHeight: 1.6,
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>
          <span style={{ color: 'var(--text3)' }}>Every</span>{' '}
          <b>1m</b>{' '}
          <span style={{ color: 'var(--text3)' }}>when count of matches in last</span>{' '}
          <b>{draft.windowSec}s</b>{' '}
          <span style={{ color: 'var(--text3)' }}>is</span>{' '}
          <b>&gt; {draft.threshold}</b>{' '}
          <span style={{ color: 'var(--text3)' }}>→ open a</span>{' '}
          <b style={{ color: sevColor }}>{draft.severity}</b>{' '}
          <span style={{ color: 'var(--text3)' }}>incident</span>
        </div>

        <label style={{ display: 'block', marginBottom: 10 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Name
          </div>
          <input value={draft.name}
            onChange={e => onChange({ ...draft, name: e.target.value })}
            placeholder={`watcher: ${query.slice(0, 32)}…`}
            style={{ width: '100%' }} />
        </label>

        {/* Trigger — runs on every evaluator tick today. Surfaced
            as a labelled, disabled control to make Watcher's
            "schedule" pane explicit, even though we only expose
            one cadence. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Trigger
          </div>
          <div style={{
            display: 'flex', alignItems: 'center', gap: 8,
            padding: '6px 8px', borderRadius: 4,
            background: 'var(--bg0)', border: '1px dashed var(--border)',
            fontSize: 12, color: 'var(--text2)',
          }}>
            <span>Every</span>
            <code style={{ color: 'var(--text)', fontFamily: 'ui-monospace, monospace' }}>1m</code>
            <span style={{ color: 'var(--text3)' }}>
              (evaluator tick — per-rule schedule coming in a later release)
            </span>
          </div>
        </div>

        {/* Input — the operator's query. Read-only; Cancel +
            re-search to tweak. Watcher's "input" pane usually
            shows a real ES query; ours surfaces the KQL the
            operator already wrote on the page. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Input — Query
          </div>
          <pre style={{
            margin: 0, padding: 8, borderRadius: 4, fontSize: 12,
            background: 'var(--bg0)', border: '1px solid var(--border)',
            fontFamily: 'ui-monospace, SFMono-Regular, monospace',
            whiteSpace: 'pre-wrap', wordBreak: 'break-all',
          }}>{query}</pre>
        </div>

        {/* Condition — threshold + window. The Watcher equivalent
            of `ctx.payload.hits.total > N over last Ws`. */}
        <div style={{ marginBottom: 12 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Condition
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
            <label>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Matches &gt;</div>
              <input type="number" min={0} value={draft.threshold}
                onChange={e => onChange({ ...draft, threshold: Number(e.target.value) || 0 })}
                style={{ width: '100%' }} />
            </label>
            <label>
              <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 4 }}>Window (sec)</div>
              <input type="number" min={10} value={draft.windowSec}
                onChange={e => onChange({ ...draft, windowSec: Number(e.target.value) || 60 })}
                style={{ width: '100%' }} />
            </label>
          </div>
        </div>

        {/* Action — severity. Channels resolved server-side by the
            notifier match rules; no per-rule channel pick here. */}
        <div style={{ marginBottom: 14 }}>
          <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--text2)',
            textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 4 }}>
            Action
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontSize: 12, color: 'var(--text3)' }}>Open a</span>
            <select value={draft.severity}
              onChange={e => onChange({ ...draft, severity: e.target.value })}
              style={{ minWidth: 130, color: sevColor, fontWeight: 600 }}>
              <option value="info">info</option>
              <option value="warning">warning</option>
              <option value="critical">critical</option>
            </select>
            <span style={{ fontSize: 12, color: 'var(--text3)' }}>
              incident · routed to matching notification channels
            </span>
          </div>
        </div>

        {msg && (
          <div style={{ marginBottom: 10, fontSize: 12,
            color: msg.kind === 'ok' ? 'var(--ok)' : 'var(--err)' }}>
            {msg.text}
          </div>
        )}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button className="sec" onClick={onClose} disabled={saving}>Cancel</button>
          <button onClick={onSave} disabled={saving}>
            {saving ? 'Saving…' : 'Create watcher'}
          </button>
        </div>
      </div>
    </div>
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
