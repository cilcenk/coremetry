import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { MultiLineChart, type DeployMarker } from './MultiLineChart';
import { EventMarkers } from './EventMarkers';
import { Spinner } from './Spinner';
import { CopilotExplain } from './CopilotExplain';
import { TracePeekDrawer } from './TracePeekDrawer';
import { IconSparkles } from './icons';
import { api } from '@/lib/api';
import { useServiceDeploys, useServiceRollouts, useSLOs } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
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

export function ServiceCharts({ service, range, onZoom }: {
  service: string;
  range: TimeRange;
  // onZoom — drag-to-select range on any of the three RED
  // panels propagates up; parent (Service.tsx) replaces the
  // page TimeRange so every chart + the operations table
  // re-fetch for the selected window. Same shape uPlot
  // emits (unix seconds).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
}) {
  // Memoise the time bounds so a render doesn't churn the
  // query keys (same trick the Logs page uses — Date.now() in
  // timeRangeToNs makes naive use unstable).
  const { from, to } = useMemo(() => timeRangeToNs(range), [range]);

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
    try {
      const v = localStorage.getItem('svc.charts.compare') as CompareMode | null;
      if (v === '24h' || v === '7d' || v === 'prev') return v;
    } catch { /* private browsing — best-effort */ }
    return 'off';
  });
  const setCompareAndPersist = (m: CompareMode) => {
    setCompare(m);
    try { localStorage.setItem('svc.charts.compare', m); }
    catch { /* best-effort */ }
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

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
    // Batch — one CH pass for rate + error_rate + p99 over the
    // same WHERE. Cold-cache time drops to ~1/3 of the legacy
    // three-call fan-out because the spans scan happens once.
    api.spanMetricBatch({
      from, to, groupBy: ['name'], dsl,
      aggs: [
        { name: 'rate',       agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99',        agg: 'p99', field: 'duration_ms' },
      ],
    }).then(res => {
      if (cancelled) return;
      setRpsSeries(res.rate       ?? []);
      setErrSeries(res.error_rate ?? []);
      setP99Series(res.p99        ?? []);
    }).catch(() => {
      if (cancelled) return;
      setRpsSeries([]); setErrSeries([]); setP99Series([]);
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [service, from, to]);

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
    const dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
    const prevFrom = from - compareOffsetNs;
    const prevTo = to - compareOffsetNs;
    api.spanMetricBatch({
      from: prevFrom, to: prevTo, groupBy: ['name'], dsl,
      aggs: [
        { name: 'rate',       agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99',        agg: 'p99', field: 'duration_ms' },
      ],
    }).then(res => {
      if (cancelled) return;
      setRpsPrev(res.rate       ?? []);
      setErrPrev(res.error_rate ?? []);
      setP99Prev(res.p99        ?? []);
    }).catch(() => {
      if (cancelled) return;
      setRpsPrev([]); setErrPrev([]); setP99Prev([]);
    });
    return () => { cancelled = true; };
  }, [service, from, to, compare, compareOffsetNs]);

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

  if (loading) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 14,
        minHeight: 200, display: 'grid', placeItems: 'center',
      }}>
        <Spinner />
      </div>
    );
  }

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
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* v0.5.480 — Each RED panel gets an EventMarkers
            overlay scoped to (service, [from, to]). The wrapper
            is position:relative so EventMarkers' inset:0 spans
            exactly the chart card; the markers sit above
            uPlot's canvas without disturbing tooltip / zoom
            interactions (pointerEvents:none on the container,
            auto on the marker line so the title-tooltip still
            works on hover). */}
        <ChartCard title="RPS by operation">
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
        </ChartCard>
        <ChartCard title="Error rate by operation">
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
        </ChartCard>
        <ChartCard title="P99 latency by operation">
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
        </ChartCard>
      </div>

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
      <button onClick={run} disabled={busy} className="sec"
        style={{ padding: '5px 12px', fontSize: 12, color: 'var(--accent2)',
                 display: 'inline-flex', alignItems: 'center', gap: 6 }}
        title={`Compare ±10 min around the latest deploy (${latest.version})`}>
        <IconSparkles /> <span>{busy ? 'Thinking…' : `Explain deploy ${latest.version}`}</span>
      </button>
      {error && (
        <div style={{
          padding: 10, borderRadius: 6, fontSize: 12,
          background: 'rgba(255,82,82,.10)', color: 'var(--err)',
          border: '1px solid rgba(255,82,82,.25)', maxWidth: 720,
        }}>{error}</div>
      )}
      {resp && (
        <div style={{
          padding: 12, borderRadius: 6, fontSize: 13, lineHeight: 1.5,
          background: 'rgba(56,139,253,.08)',
          border: '1px solid rgba(56,139,253,.25)',
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
