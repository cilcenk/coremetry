import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { MultiLineChart, type DeployMarker } from './MultiLineChart';
import { MetricPanel } from './MetricPanel';
import { OperationPicker } from './OperationPicker';
import { EventMarkers } from './EventMarkers';
import { Spinner } from './Spinner';
import { CopilotExplain } from './CopilotExplain';
import { TracePeekDrawer } from './TracePeekDrawer';
import { IconSparkles } from './icons';
import { Button } from '@/components/ui/Button';
import { api } from '@/lib/api';
import { useServiceDeploys, useServiceRollouts, useSLOs } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import { stepForWidth } from '@/lib/chartStep';
import { useContentWidth } from '@/lib/useContentWidth';
import { isResolverEligible, serviceRedDescriptors } from '@/lib/resolverEligibility';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import type { SpanMetricSeries, TimeRange } from '@/lib/types';

// ServiceCharts — three core trend panels for the focused
// service: throughput (RPS by operation), error rate (%) by
// operation, and P99 latency by operation. Pulls SLOs for the
// service and paints horizontal threshold lines on the
// matching panel (latency SLO → P99 panel; availability SLO →
// error rate panel). Pulls deploys for the service and paints
// dashed vertical markers on every chart so the operator can
// read "did this regression coincide with a deploy" in one
// glance.
//
// All three charts share a syncKey so hovering one paints the
// crosshair on the other two — Datadog dashboard convention,
// turns the three panels into one synchronised view.

export function ServiceCharts({ service, range, onZoom, opScope = '', onOpScopeChange, windowNs }: {
  service: string;
  range: TimeRange;
  // onZoom — drag-to-select range on any of the three RED
  // panels propagates up; parent (Service.tsx) replaces the
  // page TimeRange so every chart + the operations table
  // re-fetch for the selected window. Same shape uPlot
  // emits (unix seconds).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // v0.8.415 (Tempo-parity T3) — operation scope is CONTROLLED by
  // the parent (Service.tsx owns it as the ?op= URL param, house
  // rule: URL is the source of truth) so the latency heatmap and a
  // future OperationsTable row link ride the same selection.
  opScope?: string;
  onOpScopeChange?: (op: string) => void;
  // v0.8.483 — üst sayfa pencereyi çözdüyse AYNISI kullanılır:
  // timeRangeToNs göreli aralıkta Date.now()'a bağlı; iki ayrı hesap
  // ms farkıyla FARKLI RQ anahtarı üretiyor ve bundle'ın deploys
  // seed'i hiç tutmuyordu (v480'deki RED-prefetch sınıfının ikizi).
  windowNs?: { from: number; to: number };
}) {
  // Memoise the time bounds so a render doesn't churn the
  // query keys (same trick the Logs page uses — Date.now() in
  // timeRangeToNs makes naive use unstable).
  const computed = useMemo(() => timeRangeToNs(range), [range]);
  const { from, to } = windowNs ?? computed;

  // D5 doorway descriptors (v0.8.69) — each RED chart carries its MetricQuery so
  // the hover ⋮ menu opens the Explorer on that exact series. The wrap uses
  // menuOnly: these charts own drag-zoom + bucket-click, so a body-click →
  // Explore would collide. filters pin the focused service; the panels split by
  // operation, so groupBy ['name'].
  //
  // GRAN-D (v0.8.249) — these descriptors are no longer menu-only: the fetch
  // below rides the SAME objects through /api/metrics/resolve, so each panel
  // draws exactly what its doorway opens (built + eligibility-pinned in
  // lib/resolverEligibility.ts).
  // v0.8.414 (Tempo-parity T2) — operation scope. Picking an operation
  // collapses the by-operation split to that single operation and
  // upgrades the latency panel to the full percentile band (p50/p90/
  // p95/p99, agg=band) — the Grafana/Tempo RED duration panel. '' =
  // all operations (the classic split view).
  const red = useMemo(
    () => serviceRedDescriptors(service, opScope || undefined),
    [service, opScope]);
  const { rps: rpsMq, err: errMq, p99: p99Mq } = red;

  const [rpsSeries, setRpsSeries] = useState<SpanMetricSeries[] | null>(null);
  const [errSeries, setErrSeries] = useState<SpanMetricSeries[] | null>(null);
  const [p99Series, setP99Series] = useState<SpanMetricSeries[] | null>(null);
  const [loading, setLoading] = useState(true);

  // Compare-to-previous-period toggle. 'off' suppresses the
  // second fetch entirely; '24h' / '7d' / 'prev' (matched
  // window) all hit the same /api/spans/span-metric path with
  // shifted from/to. Persisted in localStorage so an operator
  // who likes the comparison view keeps it across reloads.
  const [compare, setCompare] = useState<CompareMode>(() => {
    const v = getRaw(STORAGE_KEYS.svcChartsCompare) as CompareMode | null;
    if (v === '24h' || v === '7d' || v === 'prev') return v;
    return 'off';
  });
  const setCompareAndPersist = (m: CompareMode) => {
    setCompare(m);
    setRaw(STORAGE_KEYS.svcChartsCompare, m);
  };
  const [rpsPrev, setRpsPrev] = useState<SpanMetricSeries[] | null>(null);
  const [errPrev, setErrPrev] = useState<SpanMetricSeries[] | null>(null);
  const [p99Prev, setP99Prev] = useState<SpanMetricSeries[] | null>(null);
  const compareOffsetNs = useMemo(() => {
    switch (compare) {
      case '24h':  return 24 * 3600 * 1e9;
      case '7d':   return 7 * 24 * 3600 * 1e9;
      case 'prev': return (to - from);
      default:     return 0;
    }
  }, [compare, from, to]);
  const compareLabel = compare === '24h' ? '24h ago'
    : compare === '7d' ? '7d ago'
    : compare === 'prev' ? 'prev window' : '';

  // GRAN-D (v0.8.249) — width-aware step for the resolver path. Full-row
  // charts inside #content, so the page-level bucket (GRAN-A hook) is the
  // right yardstick; entering the fetch deps below it doubles as the cache
  // key's step component (v0.5.187 — refetch on bucket crossing, never a
  // stale-step reuse). The backend min-step clamp (v0.8.243) floors it at
  // the spanmetrics tier resolution.
  const contentW = useContentWidth();
  const effStep = useMemo(
    () => stepForWidth((to - from) / 1e9, contentW),
    [from, to, contentW],
  );

  // fetchRed — one RED-triple fetch for an arbitrary window (current period
  // or the shifted compare window; both share it so main + ghost series ride
  // the same path at the same step and their buckets align).
  //
  // GRAN-D (v0.8.249) — primary path is now /api/metrics/resolve per agg:
  // the descriptors are tier-eligible by construction (service.name filter,
  // 'name' split, rate/error_rate/p99), so a 1h window draws off the
  // spanmetrics 10s/1m rollup tiers with the width-aware step instead of the
  // 12-point 5m summary read. (History note: this migration was measured and
  // parked 2026-06-15 on LATENCY grounds; it ships now for GRANULARITY, with
  // operator approval.) Dual-path: ineligible descriptors or a resolver
  // failure fall back to the pre-GRAN-D spanMetricBatch (one CH pass for all
  // three aggs over the 5m MV) — never a blank panel. Exemplars aren't
  // requested; bucket-click → api.spanExemplar covers that flow already.
  const fetchRed = useCallback(
    (fromNs: number, toNs: number): Promise<{
      rate: SpanMetricSeries[]; error_rate: SpanMetricSeries[]; p99: SpanMetricSeries[];
    }> => {
      const viaLegacy = () => {
        // Operation scope rides the DSL too; the 5m fallback has no
        // band (its tdigest is 3-quantile), so the latency slot stays
        // a single p99 line there — honest degradation, never blank.
        let dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
        if (opScope) dsl += ` AND name = "${opScope.replace(/"/g, '\\"')}"`;
        // Batch — one CH pass for rate + error_rate + p99 over the same
        // WHERE. Cold-cache time drops to ~1/3 of a three-call fan-out
        // because the spans scan happens once.
        return api.spanMetricBatch({
          from: fromNs, to: toNs, groupBy: opScope ? [] : ['name'], dsl,
          aggs: [
            { name: 'rate',       agg: 'rate' },
            { name: 'error_rate', agg: 'error_rate' },
            { name: 'p99',        agg: 'p99', field: 'duration_ms' },
          ],
        }).then(res => ({
          rate: res.rate ?? [], error_rate: res.error_rate ?? [], p99: res.p99 ?? [],
        }));
      };
      const eligible = isResolverEligible(rpsMq) && isResolverEligible(errMq) && isResolverEligible(p99Mq);
      if (!eligible) return viaLegacy();
      const r = { from: fromNs, to: toNs };
      return Promise.all([
        api.resolveMetric(rpsMq, r, { step: effStep }),
        api.resolveMetric(errMq, r, { step: effStep }),
        api.resolveMetric(p99Mq, r, { step: effStep }),
      ]).then(([rps, err, p99]) => ({
        // .series is byte-identical to the spanMetric shape (D5 contract) —
        // MultiLineChart consumes it unchanged. null result = genuinely no
        // data (not an error) → empty, matching the legacy path.
        rate: rps?.series ?? [], error_rate: err?.series ?? [], p99: p99?.series ?? [],
      })).catch(() => viaLegacy());
    },
    [service, opScope, rpsMq, errMq, p99Mq, effStep],
  );

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetchRed(from, to).then(res => {
      if (cancelled) return;
      setRpsSeries(res.rate);
      setErrSeries(res.error_rate);
      setP99Series(res.p99);
    }).catch(() => {
      if (cancelled) return;
      setRpsSeries([]); setErrSeries([]); setP99Series([]);
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [from, to, fetchRed]);

  // Compare fetch — only fires when toggle is on. Same batch
  // trick: one CH pass for the previous window's three
  // aggregates. Separate from the current-period fetch so
  // toggling compare doesn't re-fetch the current metrics
  // (which the operator is already looking at).
  useEffect(() => {
    if (compare === 'off' || compareOffsetNs === 0) {
      setRpsPrev(null); setErrPrev(null); setP99Prev(null);
      return;
    }
    let cancelled = false;
    // GRAN-D — same fetchRed dual-path as the current period, shifted by the
    // compare offset. Same step, so the ghost overlay's buckets align 1:1
    // with the main series after MultiLineChart re-bases them.
    fetchRed(from - compareOffsetNs, to - compareOffsetNs).then(res => {
      if (cancelled) return;
      setRpsPrev(res.rate);
      setErrPrev(res.error_rate);
      setP99Prev(res.p99);
    }).catch(() => {
      if (cancelled) return;
      setRpsPrev([]); setErrPrev([]); setP99Prev([]);
    });
    return () => { cancelled = true; };
  }, [from, to, compare, compareOffsetNs, fetchRed]);

  // Deploy markers for this service in the visible window.
  // deploysQ stays for DeployImpactButton (version-based AI explain;
  // auto-hidden when service.version is constant). v0.8.x — the chart
  // deploy MARKERS are now POD-CHURN rollouts, not version bumps.
  const deploysQ = useServiceDeploys(service, from, to);
  const rolloutsQ = useServiceRollouts(service, from, to);
  const deployMarkers: DeployMarker[] | undefined = useMemo(() => {
    const rollouts = rolloutsQ.data?.rollouts;
    if (!rollouts) return undefined;
    return rollouts.map(r => ({
      timeUnixNs: r.timeUnixNs,
      label: `↻ ${r.podsRemoved}p`,
      description: `rollout · ${r.podsRemoved} pod${r.podsRemoved === 1 ? '' : 's'} replaced (+${r.podsAdded})`
        + (r.versionAfter ? ` · ${r.versionBefore || '?'}→${r.versionAfter}` : ''),
    }));
  }, [rolloutsQ.data]);

  // SLO-derived thresholds for this service. Latency SLOs
  // surface on the P99 panel; availability SLOs surface on
  // the error-rate panel (as the error budget %).
  const slosQ = useSLOs();
  const { latencyThresholds, errorThresholds } = useMemo(() => {
    const lat: { value: number; label: string; severity: 'warn' | 'err' }[] = [];
    const err: { value: number; label: string; severity: 'warn' | 'err' }[] = [];
    for (const slo of slosQ.data ?? []) {
      if (slo.service !== service) continue;
      // Service-wide SLOs apply on every panel; operation-
      // scoped ones still get drawn here because the panel
      // groups by operation, so the line is meaningful when
      // the matching operation's series is on screen. The
      // label includes the operation name so the operator
      // sees which series the line belongs to.
      const opSuffix = slo.operation ? ` (${slo.operation})` : '';
      if (slo.sliType === 'latency') {
        lat.push({
          value: slo.thresholdMs,
          label: `SLO < ${slo.thresholdMs}ms${opSuffix}`,
          severity: 'err',
        });
      } else if (slo.sliType === 'availability') {
        const errBudgetPct = (1 - slo.target) * 100;
        err.push({
          value: errBudgetPct,
          label: `err ≤ ${errBudgetPct.toFixed(2)}%${opSuffix}`,
          severity: 'err',
        });
      }
    }
    return {
      latencyThresholds: lat.length > 0 ? lat : undefined,
      errorThresholds:   err.length > 0 ? err : undefined,
    };
  }, [slosQ.data, service]);

  const syncKey = `service:${service}`;

  // Spike → exemplar (v0.7.22). Clicking a point/peak on the
  // latency or error-rate chart resolves the clicked bucket
  // (ns window, computed inside MultiLineChart) to a
  // representative bad trace and opens it in the TracePeekDrawer
  // — same drawer the Logs page uses, so the operator stays in
  // context instead of a hard navigate to /trace.
  const [peekTraceId, setPeekTraceId] = useState<string | null>(null);
  // Transient, non-blocking note when a clicked bucket has no
  // matching exemplar (the operator clicked a quiet gap, or the
  // window genuinely held no slow/error spans). Auto-clears.
  const [exemplarNote, setExemplarNote] = useState<string | null>(null);
  const noteTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => { if (noteTimer.current) clearTimeout(noteTimer.current); }, []);

  // service in a ref so the bucket-click callbacks stay
  // referentially stable across renders (MultiLineChart reads
  // the live callback through a ref, but keeping these stable is
  // tidy and avoids any accidental rebuild churn).
  const serviceRef = useRef(service);
  serviceRef.current = service;

  const flashNote = useCallback((msg: string) => {
    setExemplarNote(msg);
    if (noteTimer.current) clearTimeout(noteTimer.current);
    noteTimer.current = setTimeout(() => setExemplarNote(null), 3200);
  }, []);

  const openExemplar = useCallback(
    async (kind: 'slow' | 'error', fromNs: number, toNs: number) => {
      try {
        const ex = await api.spanExemplar({
          service: serviceRef.current, from: fromNs, to: toNs, kind,
        });
        if (ex) {
          setExemplarNote(null);
          setPeekTraceId(ex.traceId);
        } else {
          flashNote(kind === 'error'
            ? 'No error trace in this bucket'
            : 'No slow trace in this bucket');
        }
      } catch {
        flashNote('Exemplar lookup failed');
      }
    },
    [flashNote],
  );

  const onLatencyBucketClick = useCallback(
    (fromNs: number, toNs: number) => { void openExemplar('slow', fromNs, toNs); },
    [openExemplar],
  );
  const onErrorBucketClick = useCallback(
    (fromNs: number, toNs: number) => { void openExemplar('error', fromNs, toNs); },
    [openExemplar],
  );

  // v0.8.420 — NO whole-component loading return. The v0.8.414 operation
  // picker lives in the toolbar and is fully controlled: every keystroke
  // commits ?op= → refetch → loading=true, and an early return here
  // replaced the ENTIRE component (focused input included) with a
  // Spinner — typing an operation name was impossible past the first
  // character. The toolbar now renders unconditionally; only the chart
  // area below swaps to the spinner.

  // Scoped titles: the split axis disappears once one operation is
  // picked, and the latency panel becomes the percentile band.
  const rpsTitle = opScope ? `RPS — ${opScope}` : 'RPS by operation';
  const errTitle = opScope ? `Error rate — ${opScope}` : 'Error rate by operation';
  const durTitle = opScope ? `Duration band — ${opScope}` : 'P99 latency by operation';

  return (
    <div style={{ marginBottom: 14 }}>
      {/* Compare-to-previous toggle row. Sits above the three
          panels so the chosen period applies to all of them
          uniformly. Dynatrace-style "previous 24h" overlay is
          off by default (no second fetch); flipping it on
          paints a dashed ghost line per chart. */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6,
        fontSize: 11, color: 'var(--text2)',
      }}>
        <span style={{
          textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700,
        }}>Compare to:</span>
        {(['off', '24h', '7d', 'prev'] as CompareMode[]).map(m => (
          <button key={m} type="button"
            onClick={() => setCompareAndPersist(m)}
            title={m === 'off' ? 'No comparison'
              : m === 'prev' ? 'Previous window of the same length'
              : `${m} ago at the same time`}
            style={{
              all: 'unset', cursor: 'pointer',
              fontSize: 11, padding: '2px 8px', borderRadius: 3,
              fontFamily: 'ui-monospace, SFMono-Regular, monospace',
              background: compare === m ? 'var(--accent2)' : 'var(--bg2)',
              color: compare === m ? 'var(--bg)' : 'var(--text2)',
              border: `1px solid ${compare === m ? 'var(--accent2)' : 'var(--border)'}`,
              fontWeight: compare === m ? 600 : 400,
            }}>
            {m === 'off' ? 'off' : m === 'prev' ? 'prev window' : m}
          </button>
        ))}
        {/* v0.8.414 (Tempo-parity T2) — operation scope. Narrows all
            three RED panels to the picked operation and upgrades the
            latency panel to the full p50–p99 percentile band, matching
            Grafana/Tempo's span-metrics RED view. Server-debounced
            picker; empty = the classic by-operation split. */}
        <span style={{
          marginLeft: 10, textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700,
        }}>Operation:</span>
        <OperationPicker
          service={service}
          value={opScope}
          onChange={op => onOpScopeChange?.(op)}
          placeholder="All operations"
          width={210} />
        <span style={{ flex: 1 }} />
        {/* AI triage button — feeds the live RED series + any
            open problems to the LLM and asks "is this service
            healthy". Distinct from per-problem explain because
            the chart may look fine and the answer should say
            so plainly. Self-hides when copilot isn't configured. */}
        <CopilotExplain
          kind="service-health"
          id={service}
          fromNs={from}
          toNs={to}
          label={<><IconSparkles /> <span>AI triage</span></>} />
        {/* Deploy impact AI — only renders when at least one
            deploy marker landed in the visible window; the
            button targets the LATEST deploy (max timeUnixNs)
            because that's the one operators most often want
            to validate post-rollout. */}
        <DeployImpactButton
          service={service}
          deploys={deploysQ.data ?? []} />
      </div>
      {/* v0.5.260 — switched 3-column grid → vertical stack.
          Uptrace / Datadog put RED triples vertically so the
          operator reads the same x-axis across all three at a
          glance instead of traversing horizontally. Each chart
          gets the full row width, more y-axis room, and the
          synced cursor (syncKey) reads top-to-bottom naturally. */}
      {/* v0.5.364 — legend-click affordance hint. The MultiLineChart
          legend already isolates a series on plain click and restores
          on second click; the Ctrl/Cmd modifier additively toggles
          for subset selection. Surface that as a small caption so
          the behaviour is discoverable instead of operator-folklore. */}
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6 }}>
        Lejantta operasyon tıkla → sadece o seri · tekrar tıkla → tümü ·
        Ctrl/⌘+tıkla → çoklu seç
      </div>
      {loading ? (
        <div style={{
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 8, padding: 14,
          minHeight: 200, display: 'grid', placeItems: 'center',
        }}>
          <Spinner />
        </div>
      ) : (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* v0.5.480 — Each RED panel gets an EventMarkers
            overlay scoped to (service, [from, to]). The wrapper
            is position:relative so EventMarkers' inset:0 spans
            exactly the chart card; the markers sit above
            uPlot's canvas without disturbing tooltip / zoom
            interactions (pointerEvents:none on the container,
            auto on the marker line so the title-tooltip still
            works on hover). */}
        <ChartCard title={rpsTitle}>
          <MetricPanel compact menuOnly title={rpsTitle} metricQuery={rpsMq}>
            <div style={{ position: 'relative' }}>
              <MultiLineChart series={rpsSeries ?? []} unit="rps"
                              height={180}
                              deploys={deployMarkers}
                              syncKey={syncKey}
                              compareSeries={rpsPrev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              compareLabel={compareLabel}
                              onZoom={onZoom} />
              <EventMarkers fromNs={from} toNs={to} service={service} />
            </div>
          </MetricPanel>
        </ChartCard>
        <ChartCard title={errTitle}>
          <MetricPanel compact menuOnly title={errTitle} metricQuery={errMq}>
            <div style={{ position: 'relative' }}>
              <MultiLineChart series={errSeries ?? []} unit="%"
                              height={180}
                              deploys={deployMarkers}
                              thresholds={errorThresholds}
                              syncKey={syncKey}
                              compareSeries={errPrev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              compareLabel={compareLabel}
                              onZoom={onZoom}
                              onBucketClick={onErrorBucketClick} />
              <EventMarkers fromNs={from} toNs={to} service={service} />
            </div>
          </MetricPanel>
        </ChartCard>
        <ChartCard title={durTitle}>
          <MetricPanel compact menuOnly title={durTitle} metricQuery={p99Mq}>
            <div style={{ position: 'relative' }}>
              <MultiLineChart series={p99Series ?? []} unit="ms"
                              height={180}
                              deploys={deployMarkers}
                              thresholds={latencyThresholds}
                              syncKey={syncKey}
                              compareSeries={p99Prev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              compareLabel={compareLabel}
                              onZoom={onZoom}
                              onBucketClick={onLatencyBucketClick} />
              <EventMarkers fromNs={from} toNs={to} service={service} />
            </div>
          </MetricPanel>
        </ChartCard>
      </div>
      )}

      {/* Spike → exemplar drawer. Opens with just the resolved
          traceId; closing clears it. Stays mounted so the close
          animation / ESC-handling matches the rest of the app. */}
      <TracePeekDrawer
        traceId={peekTraceId}
        onClose={() => setPeekTraceId(null)} />

      {/* Non-blocking "no exemplar in this bucket" affordance.
          A small fixed toast bottom-right — doesn't shift the
          chart layout, auto-dismisses after a few seconds. */}
      {exemplarNote && (
        <div role="status" aria-live="polite" style={{
          position: 'fixed', bottom: 18, right: 18, zIndex: 50,
          background: 'var(--bg2)', border: '1px solid var(--border)',
          borderRadius: 6, padding: '8px 12px', fontSize: 12,
          color: 'var(--text2)', boxShadow: '0 4px 14px rgba(0,0,0,0.35)',
          maxWidth: 280,
        }}>
          {exemplarNote}
        </div>
      )}
    </div>
  );
}

type CompareMode = 'off' | '24h' | '7d' | 'prev';

function ChartCard({ title, children }: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 10,
      minWidth: 0, // allow flex/grid children to shrink
    }}>
      <div style={{
        fontSize: 11, fontWeight: 600, color: 'var(--text2)',
        letterSpacing: '0.3px', textTransform: 'uppercase',
        marginBottom: 4,
      }}>
        {title}
      </div>
      {children}
    </div>
  );
}

// DeployImpactButton — feeds the most recent deploy in the
// visible window through /api/copilot/deploy-impact and
// renders the model's headline + raw before/after RED chips
// inline. Operator hits this AFTER a rollout to validate
// the deploy was clean before walking away. Self-hides when
// no deploys are in the window OR the copilot isn't
// configured (same gate CopilotExplain uses).
function DeployImpactButton({ service, deploys }: {
  service: string;
  deploys: import('@/lib/types').Deploy[];
}) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [resp, setResp] = useState<Awaited<ReturnType<typeof api.copilotDeployImpact>> | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.copilotConfig().then(c => setEnabled(c.enabled)).catch(() => setEnabled(false));
  }, []);
  if (enabled !== true) return null;
  if (!deploys.length) return null;

  // Latest deploy in the window — operators want the most
  // recent rollout most often.
  const latest = deploys.reduce((m, d) => d.timeUnixNs > m.timeUnixNs ? d : m, deploys[0]);

  const run = async () => {
    setBusy(true); setError(null); setResp(null);
    try {
      const r = await api.copilotDeployImpact({
        service,
        version: latest.version,
        deployTimeNs: latest.timeUnixNs,
        windowSec: 600,
      });
      setResp(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Deploy impact failed');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={{ display: 'inline-flex', flexDirection: 'column', gap: 8, alignItems: 'flex-start' }}>
      <Button variant="secondary" size="sm" onClick={run} disabled={busy}
        leftIcon={<IconSparkles />}
        style={{ color: 'var(--accent2)' }}
        title={`Compare ±10 min around the latest deploy (${latest.version})`}>
        {busy ? 'Thinking…' : `Explain deploy ${latest.version}`}
      </Button>
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'color-mix(in srgb, var(--err) 10%, transparent)', color: 'var(--err)',
          border: '1px solid color-mix(in srgb, var(--err) 25%, transparent)', maxWidth: 720,
        }}>{error}</div>
      )}
      {resp && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'color-mix(in srgb, var(--accent) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--accent) 25%, transparent)',
          color: 'var(--text)', whiteSpace: 'pre-wrap', maxWidth: 720,
        }}>
          <div style={{ fontSize: 10, color: 'var(--accent2)', marginBottom: 6, fontWeight: 700, letterSpacing: '.5px',
                        display: 'inline-flex', alignItems: 'center', gap: 4 }}>
            <IconSparkles size={11} /> DEPLOY IMPACT · {latest.version}
          </div>
          <div style={{
            display: 'flex', gap: 12, fontSize: 11,
            color: 'var(--text3)', marginBottom: 8,
            fontFamily: 'ui-monospace, monospace',
          }}>
            <span>before: {resp.before.rps.toFixed(2)} rps · {resp.before.errorRate.toFixed(2)}% err · p99 {resp.before.p99Ms.toFixed(0)}ms</span>
            <span>→</span>
            <span>after: {resp.after.rps.toFixed(2)} rps · {resp.after.errorRate.toFixed(2)}% err · p99 {resp.after.p99Ms.toFixed(0)}ms</span>
          </div>
          {resp.newOps?.length > 0 && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
              new ops: {resp.newOps.slice(0, 5).join(', ')}{resp.newOps.length > 5 ? ` (+${resp.newOps.length - 5} more)` : ''}
            </div>
          )}
          {resp.explanation}
        </div>
      )}
    </div>
  );
}
