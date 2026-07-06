import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Star } from 'lucide-react';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { passesLocalDisplayFilters } from '@/lib/serviceFilters';
import { TableSkeleton } from '@/components/Skeleton';
import { ServicePicker } from '@/components/ServicePicker';
import { Sparkline } from '@/components/Sparkline';
import { ServiceRuntimeBadge } from '@/components/ServiceRuntimeBadge';
import { useDataTable, DataTableColgroup, DataTableHead } from '@/components/DataTable';
import {
  SERVICE_COLS, DEFAULT_SERVICES_SORT,
  sanitizeServicesSort, decodeLegacyServicesSort,
} from '@/lib/servicesTable';
import { useAllServiceRuntimes, useServicesMetadata } from '@/lib/queries';
import { useTableNav } from '@/lib/useTableNav';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs, rowClickHandlers } from '@/lib/utils';
import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import { useUrlRange } from '@/lib/useUrlRange';
import { getItem, setItem } from '@/lib/storage';
import type { Service, SparklineBucket, TimeRange, SpanAgg } from '@/lib/types';

// v0.8.251 — the page's hand-rolled SortKey/NATURAL_DIR/SortTh server-sort
// system moved into the shared DataTable primitive's serverSort mode. The
// column defs (ids double as the backend's ?sort= keys), the default sort,
// the stale-id sanitizer and the legacy ?sort=&dir= link bridge live in
// lib/servicesTable.ts so the node vitest harness pins them.

export default function ServicesPage() {
  const navigate = useNavigate();
  const [range, setRange] = useUrlRange('30m');
  const [data, setData] = useState<Service[] | null | undefined>(undefined);
  const [sparklines, setSparklines] = useState<Record<string, SparklineBucket[]>>({});
  // Batch runtime fetch — one query for every service in the
  // listing, server-cached 5 min. The component renders per-row
  // ServiceRuntimeBadge inside the name cell.
  const runtimes = useAllServiceRuntimes().data;
  // Service-catalog metadata — pulled once, joined locally so
  // operators can filter the list by SRE team / owner team
  // and see "their" services. The endpoint is server-cached
  // for 60s so the per-page-load cost is bounded. Memoized on
  // the query data so the {} fallback keeps a stable identity
  // for the team-option useMemos below.
  const catalogQ = useServicesMetadata();
  const catalog = useMemo<Record<string, import('@/lib/types').ServiceMetadata>>(
    () => catalogQ.data ?? {}, [catalogQ.data]);
  // v0.5.276 — pinned services. localStorage Set; pinned rows
  // float to the top of the list regardless of sort, then sort
  // applies within both groups. Persists per-browser (per-
  // operator, basically) — no server state needed.
  const PINNED_KEY = 'coremetry-pinned-services';
  const [pinned, setPinned] = useState<Set<string>>(() => {
    const arr = getItem<string[] | null>(PINNED_KEY, null);
    return new Set(Array.isArray(arr) ? arr : []);
  });
  const togglePin = (name: string) => {
    setPinned(prev => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      setItem(PINNED_KEY, [...next]);
      return next;
    });
  };
  // Page-based pagination — 50 services per page, ranked by span
  // count server-side. Prev/Next walk the long tail; no 'Load all'
  // anymore because a single fetch of 10k+ services stalls the
  // browser and isn't useful in practice.
  const PAGE_SIZE = 50;
  const [page, setPage] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  // v0.7.44 — distinct-service total (opt-in ?withTotal=1) drives the First/Last
  // pager. null when unknown (e.g. cluster filter → raw path returns no total).
  const [total, setTotal] = useState<number | null>(null);

  // Filters (in-memory — service list is small)
  const [serviceFilter, setServiceFilter] = useState('');
  const [errorsOnly, setErrorsOnly] = useState(false);
  const [minSpans, setMinSpans] = useState('');
  const [minP99, setMinP99] = useState('');
  // Server-side team filters — resolved through the catalog
  // and applied as a service_name IN (...) allowlist on the
  // backend so the filter is correct across pages, not just
  // the visible 50.
  const [ownerTeam, setOwnerTeam] = useState('');
  const [sreTeam, setSreTeam] = useState('');
  // Cluster filter — narrows the list to services whose spans
  // emitted from the selected k8s / openshift cluster. Resolved
  // server-side via the resource/attr coalesce chain. Banks
  // running tens of clusters need this to triage by-region or
  // by-tier (prod vs canary vs DR).
  // Init cluster from `?cluster=` so a link from the Service
  // detail's per-cluster breakdown lands on the filtered list.
  const [cluster, setCluster] = useState(() => {
    const p = new URLSearchParams(window.location.search);
    return p.get('cluster') ?? '';
  });
  const [clusterOptions, setClusterOptions] = useState<string[]>([]);

  // serviceFilter is the picker's draft — typing / dropdown picks
  // mutate it freely and the in-memory `sorted` re-filter narrows
  // the visible rows live (cheap, client-side). The server-side
  // re-fetch only kicks in once the operator commits via Enter or
  // the Search button — so clicking the dropdown doesn't fan a
  // ClickHouse query out per keystroke.
  const [committedFilter, setCommittedFilter] = useState('');

  // Sort runs server-side via ?sort/&dir; this only applies
  // local display filters (errors-only / min-spans / min-p99
  // / typed substring / team match). Server returns rows
  // already ordered by the chosen column. The substring
  // filter also matches against catalog `ownerTeam` /
  // `sreTeam` so an SRE can type "platform" in the picker
  // and see every service their team owns regardless of
  // service-name spelling.
  const sorted = useMemo(() => {
    if (!data) return data;
    // Client-side REFINEMENTS only (errors-only / min-spans / min-p99). Name +
    // team filtering is server-side — the typed draft auto-commits (debounced)
    // to committedFilter → ?name across ALL services, and team dropdowns
    // resolve server-side. v0.7.29: the old local name-substring filter is gone
    // (it emptied the loaded page to "no services" until Search committed).
    const f = { errorsOnly, minSpans: parseFloat(minSpans), minP99: parseFloat(minP99) };
    const filtered = data.filter(s => passesLocalDisplayFilters(s, f));
    // v0.5.276 — pinned float to top. Server already sorted the
    // page by the chosen column; partition into [pinned, rest]
    // while preserving the server-side order within each group.
    if (pinned.size === 0) return filtered;
    const pinnedRows: typeof filtered = [];
    const restRows: typeof filtered = [];
    for (const row of filtered) {
      if (pinned.has(row.name)) pinnedRows.push(row);
      else restRows.push(row);
    }
    return [...pinnedRows, ...restRows];
  }, [data, errorsOnly, minSpans, minP99, pinned]);

  // v0.8.251 — sort state moved into the shared DataTable primitive,
  // serverSort mode: the hook owns the URL (`s_services`) + localStorage
  // persistence and the header click/arrow UX; the page owns the actual
  // ordering by forwarding dt.sort to /api/services (CH does the ORDER BY
  // before LIMIT/OFFSET, exactly as before — same column ids, same natural
  // directions). sortedRows stays the server's order verbatim, so the
  // pinned-partitioned `sorted` above renders unchanged. Legacy
  // `?sort=&dir=` links pre-date `s_services`; decodeLegacyServicesSort
  // bridges them in (above localStorage, below s_services) so old shared
  // links still land on the sender's sort. Read once at mount — the page
  // never writes the old params anymore.
  const legacySort = useMemo(() => decodeLegacyServicesSort(window.location.search), []);
  const dt = useDataTable<Service>({
    storageKey: 'services', columns: SERVICE_COLS, rows: sorted ?? [],
    serverSort: true,
    initialSort: DEFAULT_SERVICES_SORT,
    urlSortFallback: legacySort,
  });
  // Sanitized ?sort/&dir pair for the fetch below — a stale persisted id
  // (old column schema, hand-edited URL) never reaches the backend ORDER BY.
  const { sort: sortBy, dir: sortDir } = sanitizeServicesSort(dt.sort);

  // First-page fetch fires on mount. The v0.5.64 lazy-load gate
  // was removed in v0.5.72 because operators wanted the same
  // "top-N by span count" landing view every other APM ships —
  // a list view that doesn't render anything until the operator
  // commits a filter felt broken. The MV-backed page query
  // (service_summary_5m + 30s server cache + SWR) returns the
  // first 50 services in <300ms even on billion-span installs;
  // pagination handles the long tail without scaling cost.

  useEffect(() => {
    setData(undefined);
    setSparklines({});
    // v0.8.300 (quality bar S3) — cancelled flag: deps change on every
    // sort/filter/page click, and without cancellation an OLDER in-flight
    // response could resolve LAST and overwrite the fresh page
    // (stale-overwrite race).
    let cancelled = false;
    const r = timeRangeToNs(range);
    // Two-phase fetch: services list first, then sparklines scoped
    // to ONLY those names. Without the scope the sparklines payload
    // is one bucket array per service across all of them — multi-MB
    // at 10k+ services. Sparkline call is fire-and-forget so the
    // table renders even if the MV is empty.
    //
    // `committedFilter` is forwarded as ?name=… so the substring
    // search runs server-side across every service, not just the
    // current page.
    api.servicesPage(r, {
      limit: PAGE_SIZE,
      offset: page * PAGE_SIZE,
      name: committedFilter || undefined,
      // Sort runs server-side now — at 1000+ services a
      // client-side sort would only re-order the current
      // page, which made every page-load surface the same
      // top-50 by span_count even when the operator clicked
      // a different column. CH does the ORDER BY before the
      // LIMIT/OFFSET, so the page reflects the global rank.
      sort: sortBy,
      dir: sortDir,
      // Catalog-driven team filters — server resolves them
      // to a service-name allowlist, so the page is correct
      // across pagination (vs the local-only catalog match
      // that only narrowed the loaded page).
      ownerTeam: ownerTeam || undefined,
      sreTeam: sreTeam || undefined,
      cluster: cluster || undefined,
      withTotal: '1',
    }).then(resp => {
      if (cancelled) return;
      setData(resp?.services ?? []);
      setHasMore(resp?.hasMore ?? false);
      setTotal(resp?.total ?? null);
      const names = (resp?.services ?? []).map(s => s.name);
      if (names.length > 0) {
        api.serviceSparklines(r, names).then(d => { if (!cancelled) setSparklines(d ?? {}); }).catch(() => {});
      }
    }).catch(() => { if (!cancelled) { setData(null); setHasMore(false); } });
    return () => { cancelled = true; };
  }, [range, page, committedFilter, sortBy, sortDir, ownerTeam, sreTeam, cluster]);

  // Reset to page 0 whenever the search filter, time range,
  // sort, or team / cluster filter changes — staying on page 5
  // of an old result set when the operator re-orders is jarring.
  useEffect(() => { setPage(0); }, [committedFilter, range, sortBy, sortDir, ownerTeam, sreTeam, cluster]);

  // Pre-fetch the cluster options on first mount and whenever
  // the time range changes. The /api/clusters response is
  // cached server-side (60s) so flipping ranges quickly is
  // free after the first hit.
  useEffect(() => {
    const { from, to } = timeRangeToNs(range);
    api.clusters(from, to).then(r => setClusterOptions(r?.clusters ?? []))
      .catch(() => setClusterOptions([]));
  }, [range]);

  // Service combobox options come from the loaded data itself.
  const serviceOptions = useMemo(
    () => (data ?? []).map(s => s.name).sort(),
    [data]
  );

  const apply = () => setCommittedFilter(serviceFilter.trim());
  // v0.7.29 — auto-commit the typed filter after a short idle so the list
  // filters LIVE (server-side, across ALL services) without the operator having
  // to press Search. Operator-reported: typing showed "no services" because
  // only the loaded page was filtered locally until Search committed the server
  // query. Debounced (350ms) so we don't fan a ClickHouse query out per
  // keystroke; Enter / Search / dropdown-pick still commit immediately via
  // apply(). Idempotent if apply() already set the same committedFilter.
  useEffect(() => {
    const t = setTimeout(() => setCommittedFilter(serviceFilter.trim()), 350);
    return () => clearTimeout(t);
  }, [serviceFilter]);

  const reset = () => {
    setServiceFilter(''); setCommittedFilter('');
    setErrorsOnly(false); setMinSpans(''); setMinP99('');
    setOwnerTeam(''); setSreTeam('');
  };

  // Distinct team values from the catalog — feeds the two
  // dropdowns. Sorted for stable rendering.
  const ownerTeamOptions = useMemo(() => {
    const set = new Set<string>();
    for (const m of Object.values(catalog)) {
      if (m.ownerTeam) set.add(m.ownerTeam);
    }
    return [...set].sort();
  }, [catalog]);
  const sreTeamOptions = useMemo(() => {
    const set = new Set<string>();
    for (const m of Object.values(catalog)) {
      if (m.sreTeam) set.add(m.sreTeam);
    }
    return [...set].sort();
  }, [catalog]);

  // Aggregate row across the currently-visible (filtered) services.
  // Span count → sum. Error rate / avg / apdex → weighted by span count
  // so a chatty service with low latency doesn't drag the headline down
  // when a quiet but slow service is the actual outlier. P99 is the
  // max across services — there's no meaningful "average P99".
  const agg = useMemo(() => {
    if (!sorted || sorted.length === 0) return null;
    let totalSpans = 0, totalErrs = 0;
    let wAvg = 0, wApdex = 0, maxP99 = 0;
    for (const s of sorted) {
      totalSpans += s.spanCount;
      totalErrs += s.errorCount;
      wAvg += s.avgDurationMs * s.spanCount;
      wApdex += (s.apdex ?? 0) * s.spanCount;
      if (s.p99DurationMs > maxP99) maxP99 = s.p99DurationMs;
    }
    const avgMs = totalSpans > 0 ? wAvg / totalSpans : 0;
    const apdex = totalSpans > 0 ? wApdex / totalSpans : 0;
    const errorRate = totalSpans > 0 ? (totalErrs / totalSpans) * 100 : 0;
    return { spans: totalSpans, errs: totalErrs, errorRate, avgMs, p99Ms: maxP99, apdex };
  }, [sorted]);

  // Aggregate sparkline buckets — sum spans/errs across visible services
  // per timestamp; avgMs / p99Ms become weighted-by-spans / max so the
  // mini-chart stays representative.
  const aggBuckets = useMemo(() => {
    if (!sorted || sorted.length === 0) return [] as { t: number; spans: number; errs: number; avgMs: number; p99Ms: number }[];
    const merged = new Map<number, { spans: number; errs: number; avgWeighted: number; p99Max: number }>();
    for (const s of sorted) {
      for (const b of (sparklines[s.name] ?? [])) {
        const cur = merged.get(b.t) ?? { spans: 0, errs: 0, avgWeighted: 0, p99Max: 0 };
        cur.spans += b.spans;
        cur.errs += b.errs;
        cur.avgWeighted += b.avgMs * b.spans;
        if (b.p99Ms > cur.p99Max) cur.p99Max = b.p99Ms;
        merged.set(b.t, cur);
      }
    }
    return Array.from(merged.entries())
      .sort((a, b) => a[0] - b[0])
      .map(([t, v]) => ({
        t,
        spans: v.spans,
        errs: v.errs,
        avgMs: v.spans > 0 ? v.avgWeighted / v.spans : 0,
        p99Ms: v.p99Max,
      }));
  }, [sorted, sparklines]);

  const goToService = (svc: string) =>
    navigate(`/service?name=${encodeURIComponent(svc)}`);

  // Per-session hover-prefetch dedupe. Once a service has
  // been hover-prefetched for the current (range) the L1 +
  // Redis tiers stay warm well past the cache TTL so we
  // don't need to refire on every mouseenter — that would
  // hammer CH when the operator drags across 50 rows. Reset
  // whenever the range changes (keys the L1 differently).
  const prefetchedRef = useRef<Set<string>>(new Set());
  useEffect(() => { prefetchedRef.current = new Set(); }, [range]);
  const prefetchService = (name: string) => {
    if (prefetchedRef.current.has(name)) return;
    prefetchedRef.current.add(name);
    const r = timeRangeToNs(range);
    // Fire-and-forget — the response just warms the cache.
    // Catch swallows errors so a transient blip doesn't show
    // up in the console on hover.
    api.serviceBundle(name, r).catch(() => {});
  };

  // j/k row navigation. Enter / o opens the service detail.
  const tableNav = useTableNav<Service>(sorted ?? [], {
    pageId: 'services',
    onOpen: (svc) => goToService(svc.name),
  });

  // v0.6.55 — sparkline click drills to /explore carrying the
  // CLICKED metric's agg (throughput→rate, error→error_rate,
  // avg→avg, p99→p99), scoped to the service. History: v0.5.485
  // sent it to /metrics ("take me to the chart with toolbar"), but
  // v0.6.13 found /metrics renders nothing — it needs an OTel
  // metric_points key, and these sparklines are RED aggregates over
  // `spans`, not OTel metrics. v0.6.13 then routed to /service
  // detail, which dropped *which* metric the operator clicked.
  // /explore is the right surface: it renders span-aggregates AND
  // carries the agg, so the operator lands on the exact chart they
  // clicked, with the full toolbar. The service name / row body
  // still navigates to /service detail (rowClickHandlers below).
  // goToExplore('') (aggregate row) drills with no service filter
  // for the global view of that metric.
  const goToExplore = (svc: string, agg: SpanAgg) => {
    const filters = svc
      ? encodeFilters([{ k: 'service.name', op: '=', v: [svc] }])
      : '';
    const q = buildQuery([
      ['range', encodeRange(range)],
      ['filters', filters],
      ['agg', agg],
      ['field', 'duration_ms'],
      ['result', 'metric'],
    ]);
    navigate(`/explore?${q}`);
  };

  return (
    <>
      <Topbar title="Services" range={range} onRangeChange={setRange} />
      <div id="content">
        {data && data.length > 0 && (
          <div className="controls">
            <ServicePicker value={serviceFilter} onChange={setServiceFilter}
              onEnter={apply}
              placeholder="Filter services…" width={220} />
            <button onClick={apply}
                    title="Search server-side for matching services"
                    style={{ padding: '5px 12px', fontSize: 12 }}>Search</button>
            <input placeholder="Min spans" aria-label="Minimum spans" value={minSpans} type="number"
              onChange={e => setMinSpans(e.target.value)} style={{ width: 100 }} />
            <input placeholder="Min P99 (ms)" aria-label="Minimum P99 latency in milliseconds" value={minP99} type="number"
              onChange={e => setMinP99(e.target.value)} style={{ width: 110 }} />
            {/* Team dropdowns derived from the catalog. Server
                resolves the selection to a service-name
                allowlist so the filter is correct across all
                pages, not just the loaded 50 rows. */}
            <select value={ownerTeam}
              onChange={e => setOwnerTeam(e.target.value)}
              style={{ minWidth: 130 }}>
              <option value="">All owner teams</option>
              {ownerTeamOptions.map(t => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
            <select value={sreTeam}
              onChange={e => setSreTeam(e.target.value)}
              style={{ minWidth: 130 }}>
              <option value="">All SRE teams</option>
              {sreTeamOptions.map(t => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
            {/* Cluster filter — pulled from /api/clusters, which
                derives the cluster name from any of
                k8s.cluster.name / openshift.cluster.name /
                cluster (resource attr first, then span attr).
                Selecting forces the raw-span path on the backend
                since the MV doesn't carry a cluster dim — slower
                but bounded by the chosen cluster's volume. */}
            <select value={cluster}
              onChange={e => setCluster(e.target.value)}
              style={{ minWidth: 160 }}
              title={clusterOptions.length === 0
                ? 'No clusters detected — set k8s.cluster.name / openshift.cluster.name on your OTel SDK resource attrs'
                : `${clusterOptions.length} cluster${clusterOptions.length === 1 ? '' : 's'} detected`}>
              <option value="">All clusters{clusterOptions.length > 0 ? ` (${clusterOptions.length})` : ''}</option>
              {clusterOptions.map(c => (
                <option key={c} value={c}>{c}</option>
              ))}
            </select>
            <label style={{ display: 'flex', alignItems: 'center', gap: 5,
                            color: 'var(--text2)', cursor: 'pointer' }}>
              <input type="checkbox" checked={errorsOnly}
                onChange={e => setErrorsOnly(e.target.checked)} />
              Errors only
            </label>
            <button className="sec" onClick={reset}>Reset</button>
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}>
              {sorted?.length ?? 0} services{total != null ? ` · ${total} total` : ''}
              {/* v0.7.44 — First + Last jumps (Last needs the opt-in total;
                  disabled when unknown, e.g. under a cluster filter). */}
              <button className="sec" type="button"
                disabled={page === 0}
                onClick={() => setPage(0)}
                title="First page"
                style={{ padding: '3px 8px', fontSize: 11 }}>
                ⏮ First
              </button>
              <button className="sec" type="button"
                disabled={page === 0}
                onClick={() => setPage(p => Math.max(0, p - 1))}
                style={{ padding: '3px 10px', fontSize: 11 }}>
                ← Prev
              </button>
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                {page + 1}{total != null ? ` / ${Math.max(1, Math.ceil(total / PAGE_SIZE))}` : ''}
              </span>
              <button className="sec" type="button"
                disabled={!hasMore}
                onClick={() => setPage(p => p + 1)}
                style={{ padding: '3px 10px', fontSize: 11 }}>
                Next →
              </button>
              <button className="sec" type="button"
                disabled={total == null || page >= Math.ceil(total / PAGE_SIZE) - 1}
                onClick={() => { if (total != null) setPage(Math.max(0, Math.ceil(total / PAGE_SIZE) - 1)); }}
                title={total != null ? `Last page (${Math.max(1, Math.ceil(total / PAGE_SIZE))})` : 'Last page unavailable with a cluster filter'}
                style={{ padding: '3px 8px', fontSize: 11 }}>
                Last ⏭
              </button>
            </span>
          </div>
        )}

        {data === undefined && <TableSkeleton rows={10} cols={7} />}
        {data !== undefined && (!data || data.length === 0) && (
          <Empty icon="⬡" title="No services yet">
            Point your OTLP exporter at the collector — <code>OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:14318</code> (HTTP) or <code>:14317</code> (gRPC).
          </Empty>
        )}
        {data && data.length > 0 && sorted && sorted.length === 0 && (
          <Empty icon="⬡" title="No services match the current filters" />
        )}
        {sorted && sorted.length > 0 && (
          <>
            <div className="table-wrap">
              <table style={{ tableLayout: 'fixed', width: '100%' }}>
                <DataTableColgroup dt={dt} />
                {/* v0.8.251 — shared primitive header (serverSort mode): same
                    click-to-re-fetch semantics as the old SortTh row, plus the
                    URL/localStorage-persisted sort state and resize grips. */}
                <DataTableHead dt={dt} />
                <tbody>
                  {agg && (
                    <tr className="agg-row">
                      <td><span style={{ fontWeight: 700, color: 'var(--text)' }}>All ({sorted.length})</span></td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={fmtNum(agg.spans)}
                                   spark={aggBuckets.map(b => b.spans)}
                                   color="var(--accent2)"
                                   title="Total spans/5m across visible services"
                                   onClick={() => goToExplore('', 'rate')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={
                          <span className={`badge b-${agg.errorRate > 5 ? 'err' : agg.errorRate > 0 ? 'warn' : 'ok'}`}>
                            {agg.errorRate.toFixed(2)}%
                          </span>
                        }
                        spark={aggBuckets.map(b => b.spans > 0 ? (b.errs / b.spans) * 100 : 0)}
                        color="var(--err)"
                        title="Aggregate error rate (weighted by spans)"
                        onClick={() => goToExplore('', 'error_rate')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.avgMs.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.avgMs)}
                                   color="var(--accent)"
                                   title="Aggregate avg latency (weighted by spans)"
                                   onClick={() => goToExplore('', 'avg')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.p99Ms.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.p99Ms)}
                                   color="var(--warn)"
                                   title="Worst-service P99 in each bucket"
                                   onClick={() => goToExplore('', 'p99')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <ApdexBadge value={agg.apdex} />
                      </td>
                    </tr>
                  )}
                  {sorted.map((s, i) => {
                    const errCls = s.errorRate > 5 ? 'err' : s.errorRate > 0 ? 'warn' : 'ok';
                    const buckets = sparklines[s.name] ?? [];
                    const isSelected = tableNav.selected === i;
                    return (
                      <tr key={s.name}
                          data-row-idx={i}
                          className={isSelected ? 'row-selected' : undefined}
                          onMouseEnter={() => {
                            tableNav.setSelected(i);
                            // Hover prefetch — fire the bundle
                            // query for this service so by the
                            // time the operator clicks the row
                            // the L1 + Redis tiers are warm and
                            // the detail page mount lands on a
                            // HIT-L1. Dedupe via a Set so a
                            // mouse drag across 10 rows fires 10
                            // requests, not 100.
                            prefetchService(s.name);
                          }}
                          {...rowClickHandlers(`/service?name=${encodeURIComponent(s.name)}`,
                                               () => goToService(s.name))}>
                        <td>
                          {/* v0.5.276 — pin star. Click toggles
                              localStorage; pinned services float to
                              the top of the list regardless of
                              sort. Operator's 3-5 daily-touched
                              services stay sticky. */}
                          <button type="button"
                            onClick={e => { e.stopPropagation(); togglePin(s.name); }}
                            title={pinned.has(s.name)
                              ? 'Unpin — service falls back into the sorted list'
                              : 'Pin — float to top of the list'}
                            style={{
                              all: 'unset', cursor: 'pointer',
                              marginRight: 6, verticalAlign: 'middle',
                              color: pinned.has(s.name) ? 'var(--warn)' : 'var(--text3)',
                              opacity: pinned.has(s.name) ? 1 : 0.4,
                              transition: 'opacity .15s, color .15s',
                            }}>
                            <Star size={14} strokeWidth={1.75}
                              fill={pinned.has(s.name) ? 'currentColor' : 'none'} />
                          </button>
                          {/* v0.5.274 — auto-scored health dot.
                              Red/yellow/green from errorRate +
                              open problem counts (computed
                              server-side at read time). Title
                              surfaces the firing rule so the
                              badge is auditable. */}
                          <HealthDot health={s.health} reason={s.healthReason}
                            openProblems={s.openProblems} />
                          <span style={{ fontWeight: 600 }}>{s.name}</span>
                          {/* Runtime fingerprint pill — pulled from
                              the per-list batch fetch (one query
                              for every service vs N queries per
                              row). Compact mode = small font, no
                              glyph, just the language-coloured
                              text. Hidden when the SDK didn't
                              emit usable resource attributes. */}
                          {runtimes && runtimes[s.name] && (
                            <ServiceRuntimeBadge rt={runtimes[s.name]} compact
                                                 style={{ marginLeft: 8 }} />
                          )}
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={fmtNum(s.spanCount)}
                                     spark={buckets.map(b => b.spans)}
                                     color="var(--accent2)"
                                     title={`Spans/5m for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'rate')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={
                            <span className={`badge b-${errCls === 'err' ? 'err' : errCls === 'warn' ? 'warn' : 'ok'}`}>
                              {s.errorRate.toFixed(2)}%
                            </span>
                          }
                          spark={buckets.map(b => b.spans > 0 ? (b.errs / b.spans) * 100 : 0)}
                          color="var(--err)"
                          title={`Error rate (%) for ${s.name}`}
                          onClick={() => goToExplore(s.name, 'error_rate')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.avgDurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.avgMs)}
                                     color="var(--accent)"
                                     title={`Avg latency (ms) for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'avg')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.p99DurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.p99Ms)}
                                     color="var(--warn)"
                                     title={`P99 latency (ms) for ${s.name}`}
                                     onClick={() => goToExplore(s.name, 'p99')} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <ApdexBadge value={s.apdex} />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <div style={{ marginTop: 10, fontSize: 12, color: 'var(--text3)' }}>
              {sorted.length} services · sorted by <b style={{ color: 'var(--accent2)' }}>{sortBy}</b> {sortDir}
            </div>
          </>
        )}
      </div>
    </>
  );
}

// SparkCell renders the existing numeric value next to a small inline
// sparkline. The sparkline area drills to /service when clicked.
//
// v0.6.14 — pass onClick directly to <Sparkline> so the SVG element
// handles the click itself. Pre-v0.6.14 the wrapper <span> caught
// the click while Sparkline's SVG (with no onClick prop) styled
// itself `cursor: default`, sitting on top of the span and hiding
// the pointer cursor — operators reported "sparkline tıklanmıyor"
// (sparkline doesn't even appear clickable). The Sparkline
// component (v0.5.485) already supports onClick + cursor:pointer
// when set; using its native affordance is the fix.
//
// We still stop propagation on the wrapping span so a click on the
// thin gap between the value text and the SVG doesn't double-fire
// (row-level handler + sparkline handler).
function SparkCell({
  value, spark, color, title, onClick,
}: {
  value: React.ReactNode;
  spark: number[];
  color: string;
  title: string;
  onClick: () => void;
}) {
  return (
    // Whole-cell click target (value + sparkline) so the operator can
    // aim at the number or the spark and still land on the metric
    // chart. stopPropagation keeps the row-level nav (→ service
    // detail) from firing underneath — a click on the metric cell
    // means "chart this metric", not "open the service".
    <span
      onClick={(e) => { e.stopPropagation(); onClick(); }}
      style={{ display: 'inline-flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end', cursor: 'pointer' }}>
      <span>{value}</span>
      <Sparkline
        values={spark}
        color={color}
        title={`${title} — click to chart in Explore`}
        onClick={onClick}
      />
    </span>
  );
}

// Apdex score → coloured badge.
//   ≥ 0.94  Excellent (ok)
//   ≥ 0.85  Good (info)
//   ≥ 0.70  Fair (warn)
//   <  0.70 Poor (err)
function ApdexBadge({ value }: { value: number }) {
  if (value == null || isNaN(value)) return <span style={{ color: 'var(--text3)' }}>—</span>;
  const cls = value >= 0.94 ? 'b-ok'
            : value >= 0.85 ? 'b-info'
            : value >= 0.70 ? 'b-warn'
            : 'b-err';
  return <span className={`badge ${cls}`}>{value.toFixed(2)}</span>;
}

// HealthDot — v0.5.274 Datadog-Watchdog-style service health
// pill. 8×8 dot colored by the server-computed health verdict;
// tooltip surfaces the firing rule so the operator can argue
// with the badge. Missing health (older row / problem-count
// lookup failed) renders nothing — fail-soft.
function HealthDot({ health, reason, openProblems }: {
  health?: 'green' | 'yellow' | 'red';
  reason?: string;
  openProblems?: number;
}) {
  if (!health) return null;
  const color = health === 'red' ? 'var(--err)'
              : health === 'yellow' ? 'var(--warn)'
              : 'var(--ok)';
  const title = reason
    ? `${health.toUpperCase()} · ${reason}${openProblems ? ` · ${openProblems} open problem${openProblems === 1 ? '' : 's'}` : ''}`
    : `${health.toUpperCase()} · healthy${openProblems ? ` · ${openProblems} open` : ''}`;
  return (
    <span title={title}
      style={{
        display: 'inline-block', width: 8, height: 8,
        borderRadius: '50%', background: color,
        marginRight: 8, verticalAlign: 'middle',
        boxShadow: health === 'red'
          ? '0 0 0 2px color-mix(in srgb, var(--err) 20%, transparent)'
          : health === 'yellow'
          ? '0 0 0 2px color-mix(in srgb, var(--warn) 18%, transparent)'
          : 'none',
      }} />
  );
}
