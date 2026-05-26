import { useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Topbar } from '@/components/Topbar';
import { Spinner, Empty } from '@/components/Spinner';
import { TableSkeleton } from '@/components/Skeleton';
import { ServicePicker } from '@/components/ServicePicker';
import { Sparkline } from '@/components/Sparkline';
import { ServiceRuntimeBadge } from '@/components/ServiceRuntimeBadge';
import { useAllServiceRuntimes } from '@/lib/queries';
import { useTableNav } from '@/lib/useTableNav';
import { api } from '@/lib/api';
import { fmtNum, timeRangeToNs, rowClickHandlers } from '@/lib/utils';
import { encodeRange, encodeFilters, buildQuery } from '@/lib/urlState';
import type { Service, SparklineBucket, TimeRange, SpanAgg } from '@/lib/types';

type SortKey = 'name' | 'spanCount' | 'errorRate' | 'avg' | 'p99' | 'apdex';
type SortDir = 'asc' | 'desc';

// Each column's natural starting direction when first selected.
// Apdex is a satisfaction score so 'asc' surfaces the WORST services first.
const NATURAL_DIR: Record<SortKey, SortDir> = {
  name: 'asc',
  spanCount: 'desc',
  errorRate: 'desc',
  avg: 'desc',
  p99: 'desc',
  apdex: 'asc',
};

export default function ServicesPage() {
  const navigate = useNavigate();
  const [range, setRange] = useState<TimeRange>({ preset: '30m' });
  const [data, setData] = useState<Service[] | null | undefined>(undefined);
  const [sparklines, setSparklines] = useState<Record<string, SparklineBucket[]>>({});
  // Batch runtime fetch — one query for every service in the
  // listing, server-cached 5 min. The component renders per-row
  // ServiceRuntimeBadge inside the name cell.
  const runtimes = useAllServiceRuntimes().data;
  // Service-catalog metadata — pulled once, joined locally so
  // operators can filter the list by SRE team / owner team
  // and see "their" services. The endpoint is server-cached
  // for 60s so the per-page-load cost is bounded.
  const [catalog, setCatalog] = useState<Record<string, import('@/lib/types').ServiceMetadata>>({});
  useEffect(() => {
    api.servicesMetadata().then(c => setCatalog(c ?? {})).catch(() => {});
  }, []);
  const [sortBy, setSortBy] = useState<SortKey>('errorRate');
  const [sortDir, setSortDir] = useState<SortDir>('desc');
  // v0.5.276 — pinned services. localStorage Set; pinned rows
  // float to the top of the list regardless of sort, then sort
  // applies within both groups. Persists per-browser (per-
  // operator, basically) — no server state needed.
  const PINNED_KEY = 'coremetry-pinned-services';
  const [pinned, setPinned] = useState<Set<string>>(() => {
    try {
      const raw = localStorage.getItem(PINNED_KEY);
      if (!raw) return new Set();
      const arr = JSON.parse(raw);
      return new Set(Array.isArray(arr) ? arr : []);
    } catch { return new Set(); }
  });
  const togglePin = (name: string) => {
    setPinned(prev => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      try { localStorage.setItem(PINNED_KEY, JSON.stringify([...next])); }
      catch { /* quota / private mode — silently no-op */ }
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
    }).then(resp => {
      setData(resp?.services ?? []);
      setHasMore(resp?.hasMore ?? false);
      const names = (resp?.services ?? []).map(s => s.name);
      if (names.length > 0) {
        api.serviceSparklines(r, names).then(d => setSparklines(d ?? {})).catch(() => {});
      }
    }).catch(() => { setData(null); setHasMore(false); });
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
    const minS = parseFloat(minSpans);
    const minP = parseFloat(minP99);
    const term = serviceFilter.trim().toLowerCase();
    const filtered = data.filter(s => {
      if (term) {
        const md = catalog[s.name];
        const matches =
          s.name.toLowerCase().includes(term) ||
          (md?.ownerTeam ?? '').toLowerCase().includes(term) ||
          (md?.sreTeam ?? '').toLowerCase().includes(term);
        if (!matches) return false;
      }
      if (errorsOnly && !(s.errorCount > 0 || s.errorRate > 0)) return false;
      if (!isNaN(minS) && s.spanCount < minS) return false;
      if (!isNaN(minP) && s.p99DurationMs < minP) return false;
      return true;
    });
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
  }, [data, serviceFilter, errorsOnly, minSpans, minP99, catalog, pinned]);

  const apply = () => setCommittedFilter(serviceFilter.trim());

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

  const toggleSort = (col: SortKey) => {
    if (sortBy === col) {
      setSortDir(sortDir === 'desc' ? 'asc' : 'desc');
    } else {
      setSortBy(col);
      setSortDir(NATURAL_DIR[col]);
    }
  };

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

  // v0.5.485 — sparkline click → /metrics (operator-requested).
  // Was /explore (DSL playground) which has wider scope; metric
  // explorer is the right destination for "I clicked the avg-
  // response sparkline, take me to the chart with toolbar".
  // We carry service + range; operator picks the metric on the
  // landing page (MetricNamePicker autocompletes from their
  // index). The legacy goToExplore is kept around so other
  // call sites that genuinely want the DSL playground still
  // have a path.
  const goToMetricExplorer = (svc: string) => {
    const parts: string[] = [];
    if (svc) parts.push(`service=${encodeURIComponent(svc)}`);
    parts.push(`range=${encodeURIComponent(encodeRange(range))}`);
    navigate(`/metrics?${parts.join('&')}`);
  };
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
              placeholder="Service… (Enter to search)" width={220} />
            <button onClick={apply}
                    title="Search server-side for matching services"
                    style={{ padding: '5px 12px', fontSize: 12 }}>Search</button>
            <input placeholder="Min spans" value={minSpans} type="number"
              onChange={e => setMinSpans(e.target.value)} style={{ width: 100 }} />
            <input placeholder="Min P99 (ms)" value={minP99} type="number"
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
            <span style={{ color: 'var(--text3)', fontSize: 12, marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>
              {sorted?.length ?? 0} services on page {page + 1}
              <button className="sec" type="button"
                disabled={page === 0}
                onClick={() => setPage(p => Math.max(0, p - 1))}
                style={{ padding: '3px 10px', fontSize: 11 }}>
                ← Prev
              </button>
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                {page + 1}
              </span>
              <button className="sec" type="button"
                disabled={!hasMore}
                onClick={() => setPage(p => p + 1)}
                style={{ padding: '3px 10px', fontSize: 11 }}>
                Next →
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
              <table>
                <thead>
                  <tr>
                    <SortTh col="name"      label="Service"    sort={sortBy} dir={sortDir} onSort={toggleSort} />
                    <SortTh col="spanCount" label="Spans"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="errorRate" label="Error rate" sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="avg"       label="Avg"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="p99"       label="P99"        sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                    <SortTh col="apdex"     label="Apdex"      sort={sortBy} dir={sortDir} onSort={toggleSort} align="right" />
                  </tr>
                </thead>
                <tbody>
                  {agg && (
                    <tr className="agg-row">
                      <td><span style={{ fontWeight: 700, color: 'var(--text)' }}>All ({sorted.length})</span></td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={fmtNum(agg.spans)}
                                   spark={aggBuckets.map(b => b.spans)}
                                   color="var(--accent2)"
                                   title="Total spans/5m across visible services"
                                   onClick={() => goToMetricExplorer('')} />
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
                        onClick={() => goToMetricExplorer('')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.avgMs.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.avgMs)}
                                   color="var(--accent)"
                                   title="Aggregate avg latency (weighted by spans)"
                                   onClick={() => goToMetricExplorer('')} />
                      </td>
                      <td className="mono" style={{ textAlign: 'right' }}>
                        <SparkCell value={`${agg.p99Ms.toFixed(1)}ms`}
                                   spark={aggBuckets.map(b => b.p99Ms)}
                                   color="var(--warn)"
                                   title="Worst-service P99 in each bucket"
                                   onClick={() => goToMetricExplorer('')} />
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
                              fontSize: 13, marginRight: 6,
                              color: pinned.has(s.name) ? 'var(--warn, #facc15)' : 'var(--text3)',
                              opacity: pinned.has(s.name) ? 1 : 0.4,
                              transition: 'opacity .15s, color .15s',
                            }}>
                            {pinned.has(s.name) ? '★' : '☆'}
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
                                     onClick={() => goToMetricExplorer(s.name)} />
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
                          onClick={() => goToMetricExplorer(s.name)} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.avgDurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.avgMs)}
                                     color="var(--accent)"
                                     title={`Avg latency (ms) for ${s.name}`}
                                     onClick={() => goToMetricExplorer(s.name)} />
                        </td>
                        <td className="mono" style={{ textAlign: 'right' }}>
                          <SparkCell value={`${s.p99DurationMs.toFixed(1)}ms`}
                                     spark={buckets.map(b => b.p99Ms)}
                                     color="var(--warn)"
                                     title={`P99 latency (ms) for ${s.name}`}
                                     onClick={() => goToMetricExplorer(s.name)} />
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
// sparkline. The sparkline area swallows the row click so the user
// drills into Explore with the right aggregation pre-selected, instead
// of navigating to /service.
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
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end' }}>
      <span>{value}</span>
      <span
        onClick={e => { e.stopPropagation(); onClick(); }}
        title={title}
        style={{ display: 'inline-block' }}
      >
        <Sparkline values={spark} color={color} title={title} />
      </span>
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

function SortTh({ col, label, sort, dir, onSort, align }: {
  col: SortKey; label: string;
  sort: SortKey; dir: SortDir;
  onSort: (c: SortKey) => void;
  align?: 'left' | 'right';
}) {
  const active = sort === col;
  return (
    <th className={`sortable${active ? ' sorted' : ''}`}
        onClick={() => onSort(col)}
        style={{ textAlign: align ?? 'left' }}>
      {label}
      <span className="sort-arrow">{active ? (dir === 'desc' ? '▼' : '▲') : '↕'}</span>
    </th>
  );
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
              : health === 'yellow' ? 'var(--warn, #facc15)'
              : 'var(--ok, #22c55e)';
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
          ? '0 0 0 2px rgba(220,38,38,0.20)'
          : health === 'yellow'
          ? '0 0 0 2px rgba(250,204,21,0.18)'
          : 'none',
      }} />
  );
}
