import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { MultiLineChart, type DeployMarker } from './MultiLineChart';
import { MetricPanel } from './MetricPanel';
import { OperationPicker } from './OperationPicker';
import { Spinner } from './Spinner';
import { AIExplainButton } from './ai/AIExplainButton';
import { TracePeekDrawer } from './TracePeekDrawer';
import { IconSparkles } from './icons';
import { Button } from '@/components/ui/Button';
import { api } from '@/lib/api';
import { useServiceDeploys, useServiceRollouts, useSLOs } from '@/lib/queries';
import { timeRangeToNs } from '@/lib/utils';
import { quantizeWidth, stepForWidth } from '@/lib/chartStep';
import { useContentWidth } from '@/lib/useContentWidth';
import { isResolverEligible, serviceRedDescriptors } from '@/lib/resolverEligibility';
import { metricQuery } from '@/lib/metricQuery';
import { defaultLatencyHidden } from '@/lib/chart/legendVisibility';
import { getRaw, setRaw, STORAGE_KEYS } from '@/lib/storage';
import type { ChartTimeRegion } from '@/lib/chart/overlays';
import type { Problem, SpanMetricSeries, TimeRange } from '@/lib/types';
import { QueryError } from '@/components/QueryError';

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

export function ServiceCharts({ service, range, onZoom, onZoomReset, opScope = '', onOpScopeChange, windowNs, problems, rootOnly = false }: {
  service: string;
  range: TimeRange;
  // rootOnly (v0.9.348) — narrow the per-operation series to the service's
  // OWN entry points (span kind server/consumer) instead of every span it
  // emits.
  //
  // Unfiltered, an api-gateway's chart draws its OUTBOUND calls
  // (account-service/ListAccounts, forex-service/GetRates) as if they were
  // its own load — measured on the demo: 22 series, of which only 14 are
  // this service's endpoints. Operator: "sadece root operationlar olsun."
  // Same standing entry-span principle the service metrics already follow.
  //
  // OPT-IN on purpose. ServiceCharts has four consumers (Service Details,
  // Service Overview, clusters/TrendPanel, RuntimeCharts) and Overview owns
  // its own "Throughput · giriş / tüm span'ler" distinction. Burying the
  // filter inside the component would silently change all four.
  //
  // Costs nothing: it is one more conjunct on a DSL the batch endpoint
  // already parses (`kind` is a mapped field — chstore/filterexpr.go).
  rootOnly?: boolean;
  // onZoom — drag-to-select range on any of the three RED
  // panels propagates up; parent (Service.tsx) replaces the
  // page TimeRange so every chart + the operations table
  // re-fetch for the selected window. Same shape uPlot
  // emits (unix seconds).
  onZoom?: (fromUnixSec: number, toUnixSec: number) => void;
  // Grafana-parite M1 — çift-tık: Service.tsx zoom geri-yığınını pop eder
  // (üç RED paneline aynen iletilir).
  onZoomReset?: () => void;
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
  // Grafana-parite M3 — sayfanın ZATEN elindeki problem listesi (Service.tsx
  // service-bundle'ı; ek fetch YOK). Açık problem pencereleri üç RED paneline
  // x-bölgesi (kırmızı gölge + üst şerit + P1/severity etiketi) olarak biner.
  problems?: Problem[];
}) {
  // Memoise the time bounds so a render doesn't churn the
  // query keys (same trick the Logs page uses — Date.now() in
  // timeRangeToNs makes naive use unstable).
  const computed = useMemo(() => timeRangeToNs(range), [range]);
  // v0.9.83 — x-ekseni sorgu penceresine sabit (uPlot Aşama 2 madde 2).
  const xRangeSec = useMemo(() => {
    const w = windowNs ?? computed;
    return { from: w.from / 1e9, to: w.to / 1e9 };
  }, [windowNs, computed]);
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
  // Madde 4 sweep (operatör onayı) — scoped duration-band paneline avg
  // çizgisi: band p50/p90/p95/p99 verir, avg ayrı (eligible-by-construction)
  // descriptor'la gelir ve 'avg' etiketiyle aynı panele biner. By-op modda
  // (etiketler operasyon adları) avg anlamsız → null.
  const avgMq = useMemo(
    () => opScope ? metricQuery({
      metric: 'duration_milliseconds_bucket', agg: 'avg', unit: 'ms',
      filters: { 'service.name': service, name: opScope }, groupBy: [],
    }) : null,
    [service, opScope]);

  const [rpsSeries, setRpsSeries] = useState<SpanMetricSeries[] | null>(null);
  const [errSeries, setErrSeries] = useState<SpanMetricSeries[] | null>(null);
  const [p99Series, setP99Series] = useState<SpanMetricSeries[] | null>(null);
  const [loading, setLoading] = useState(true);
  // v0.9.858 (UX denetimi K6) — RED fetch hatası + Retry nonce.
  const [redErr, setRedErr] = useState<string | null>(null);
  const [redRetry, setRedRetry] = useState(0);

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
  // v0.9.844 — `compareLabel` ("24h ago" / "7d ago" / "prev window") SİLİNDİ.
  // Tek tüketicisi eski MLC gövdesinin lejant ekiydi; CorePanel hayaleti
  // kendi " (önceki)" ekini basar (v0.9.764'ten beri ghost dilinin tek
  // sahibi orası), yani prop v2'de zaten yok sayılıyordu.

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
      // Madde 4 sweep — scoped modda avg serisi 'avg' etiketiyle latency
      // slot'una eklenir (band'ın p50/p90/p95/p99 çizgilerinin yanına).
      const tagAvg = (s: SpanMetricSeries[]): SpanMetricSeries[] =>
        s.map(x => ({ ...x, groupKey: ['avg'] }));
      const viaLegacy = () => {
        // Operation scope rides the DSL too; the 5m fallback has no
        // band (its tdigest is 3-quantile), so the latency slot stays
        // a single p99 line there (+ avg, madde 4) — honest degradation,
        // never blank.
        let dsl = `service.name = "${service.replace(/"/g, '\\"')}"`;
        if (opScope) dsl += ` AND name = "${opScope.replace(/"/g, '\\"')}"`;
        // v0.9.348 — root/entry narrow. Verified live: 22 series → 14, which
        // matches ClickHouse's own 14 server + 8 client split for the same
        // service and window.
        if (rootOnly) dsl += ` AND kind in [server, consumer]`;
        // Batch — one CH pass for rate + error_rate + p99 over the same
        // WHERE. Cold-cache time drops to ~1/3 of a three-call fan-out
        // because the spans scan happens once.
        return api.spanMetricBatch({
          from: fromNs, to: toNs, groupBy: opScope ? [] : ['name'], dsl,
          // v0.9.391 — effStep zaten px-adaptif (stepForWidth); mdp ek
          // emniyet: explicit step sunucu bütçesinden de geçer. quantizeWidth
          // ŞART: ham piksel sunucu cache anahtarını piksel başına bölerdi
          // (v0.8.270 sınırlı-kardinalite kuralı).
          maxDataPoints: contentW > 0 ? Math.round(quantizeWidth(contentW) / 2) : undefined,
          aggs: [
            { name: 'rate',       agg: 'rate' },
            { name: 'error_rate', agg: 'error_rate' },
            { name: 'p99',        agg: 'p99', field: 'duration_ms' },
            ...(opScope ? [{ name: 'avg', agg: 'avg', field: 'duration_ms' }] : []),
          ],
        }).then(({ series: res }) => ({
          rate: res.rate ?? [], error_rate: res.error_rate ?? [],
          p99: [...tagAvg(res.avg ?? []), ...(res.p99 ?? [])],
        }));
      };
      // v0.9.348 — rootOnly forces the DSL path, and this is load-bearing.
      //
      // The resolver builds its WHERE from MetricQuery.Filters, which is
      // Record<string,string> and renders `col = ?` with ONE value
      // (chstore/metricresolve.go). "server OR consumer" cannot be expressed
      // there. If the resolver stayed eligible, the root narrow would apply on
      // the DSL path and SILENTLY VANISH on the resolver path — the same
      // two-paths-one-predicate trap v0.9.345 fixed on /services, where the
      // operator switches paths just by changing an unrelated input.
      //
      // Falling back costs one bounded spanMetricBatch call (a single CH pass
      // for all three aggs), which is the path this component used before the
      // resolver existed.
      const eligible = !rootOnly
        && isResolverEligible(rpsMq) && isResolverEligible(errMq) && isResolverEligible(p99Mq)
        && (!avgMq || isResolverEligible(avgMq));
      if (!eligible) return viaLegacy();
      const r = { from: fromNs, to: toNs };
      return Promise.all([
        api.resolveMetric(rpsMq, r, { step: effStep }),
        api.resolveMetric(errMq, r, { step: effStep }),
        api.resolveMetric(p99Mq, r, { step: effStep }),
        avgMq ? api.resolveMetric(avgMq, r, { step: effStep }) : Promise.resolve(null),
      ]).then(([rps, err, p99, avg]) => ({
        // .series is byte-identical to the spanMetric shape (D5 contract) —
        // MultiLineChart consumes it unchanged. null result = genuinely no
        // data (not an error) → empty, matching the legacy path.
        rate: rps?.series ?? [], error_rate: err?.series ?? [],
        p99: [...tagAvg(avg?.series ?? []), ...(p99?.series ?? [])],
      })).catch(() => viaLegacy());
    },
    // v0.9.348 — rootOnly joins the deps: it changes both the DSL and which
    // path runs, so a stale closure would keep serving the unfiltered series.
    [service, opScope, rpsMq, errMq, p99Mq, avgMq, effStep, rootOnly],
  );

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetchRed(from, to).then(res => {
      if (cancelled) return;
      setRedErr(null);
      setRpsSeries(res.rate);
      setErrSeries(res.error_rate);
      setP99Series(res.p99);
    }).catch(e => {
      if (cancelled) return;
      // v0.9.858 (UX denetimi K6) — hata BOŞ SERİYE eziliyordu: üç RED
      // grafiği boş çiziliyor, servis "trafik yok" gibi okunuyordu. En
      // yanıltıcı boş-durum: grafik "sağlıklı sessizlik" izlenimi verir.
      setRpsSeries([]); setErrSeries([]); setP99Series([]);
      setRedErr(e instanceof Error ? e.message : String(e));
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => { cancelled = true; };
  }, [from, to, fetchRed, redRetry]);

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

  // Grafana-parite M3 — açık problem pencereleri → chart x-bölgeleri.
  // toSec AÇIK problemde pencere SONUna sabitlenir ('now' her render değişip
  // build imzasını churn eder; bölge zaten canlı x-ölçeğine kırpılıyor).
  // startedAt pencereden eskiyse drawTimeRegions sola kırpar.
  const problemRegions = useMemo<ChartTimeRegion[] | undefined>(() => {
    const open = (problems ?? []).filter(p => p.status === 'open' && p.service === service);
    if (open.length === 0) return undefined;
    return open.map(p => ({
      fromSec: p.startedAt / 1e9,
      toSec: to / 1e9,
      color: 'var(--err)',
      label: p.priority ?? p.severity.toUpperCase(),
    }));
  }, [problems, service, to]);

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
      {/* v0.9.380 (redesign D4) — toolbar ikiye ayrıldı: SOL küme scope
          (Compare + Operation), SAĞ küme eylemler (AI triage + Explain
          deploy). Dört kontrol ailesi tek düz flex satırında okunmuyordu;
          iki küme arasına esnek boşluk girdi, dar ekranda kümeler bütün
          hâlde alt satıra sarar (flexWrap). */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6,
        fontSize: 11, color: 'var(--text2)', flexWrap: 'wrap', rowGap: 6,
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
        {/* v0.9.477 — cevap sağ AI çekmecesinde. service-health prompt'u
            CANLI pencereye bağlı olduğundan from/to da linke yazılır:
            paylaşılan `?ai=service-health:<svc>:<from>:<to>` aynı pencereyi
            açıklar, "başka bir saati anlatan link" olmaz. */}
        <AIExplainButton
          subject={{ kind: 'service-health', id: service, fromNs: from, toNs: to }}
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
      {/* v0.9.380 (D4) — v0.5.364'ün kalıcı lejant-ipucu satırı ⓘ'ye
          taşındı: bir kez öğrenilen davranış için sürekli dikey alan
          harcanıyordu; kazanılan alan grafiklere döndü. */}
      <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 6, display: 'flex', alignItems: 'center', gap: 6 }}>
        <span title="Lejantta operasyon tıkla → sadece o seri · tekrar tıkla → tümü · Ctrl/⌘+tıkla → çoklu seç"
          style={{ cursor: 'help', border: '1px solid var(--border)', borderRadius: '50%', width: 15, height: 15, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', fontSize: 10 }}>ⓘ</span>
        <span>lejant etkileşimleri</span>
      </div>
      {loading ? (
        <div style={{
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 8, padding: 14,
          minHeight: 200, display: 'grid', placeItems: 'center',
        }}>
          <Spinner />
        </div>
      ) : redErr ? (
        <div style={{
          background: 'var(--bg1)', border: '1px solid var(--border)',
          borderRadius: 8, padding: 14,
          minHeight: 200, display: 'grid', placeItems: 'center',
        }}>
          <QueryError message={redErr} onRetry={() => setRedRetry(n => n + 1)}>
            The RED series could not be loaded. Empty charts here would have
            read as "no traffic".
          </QueryError>
        </div>
      ) : (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* v0.9.396 (Ş3-a) — v0.5.480'in panel-başına EventMarkers
            overlay'i SÖKÜLDÜ: annotation şeridi (v0.9.395, yığının
            altında) aynı olayları daha zengin gösteriyor (popover +
            hedef link + tık-zoom) ve overlay'in 3 AYRI
            /api/operator-events fetch'i çift işti; olaylar hem çizgi
            hem şeritte iki kez görünüyordu. Bileşen dosyası ve
            Endpoints/OperationsTable kullanımları DURUYOR (oralarda
            şerit yok henüz) — kolay revert. */}
        <ChartCard title={rpsTitle}>
          <MetricPanel compact menuOnly title={rpsTitle} metricQuery={rpsMq}>
            <div style={{ position: 'relative' }}>
              <MultiLineChart xRange={xRangeSec} series={rpsSeries ?? []} unit="rps"
                              height={180}
                              deploys={deployMarkers}
                              regions={problemRegions}
                              syncKey={syncKey}
                              compareSeries={rpsPrev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              onZoom={onZoom}
                              onZoomReset={onZoomReset} />
            </div>
          </MetricPanel>
        </ChartCard>
        <ChartCard title={errTitle}>
          <MetricPanel compact menuOnly title={errTitle} metricQuery={errMq}>
            <div style={{ position: 'relative' }}>
              <MultiLineChart xRange={xRangeSec} series={errSeries ?? []} unit="%"
                              height={180}
                              deploys={deployMarkers}
                              thresholds={errorThresholds}
                              regions={problemRegions}
                              syncKey={syncKey}
                              compareSeries={errPrev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              onZoom={onZoom}
                              onZoomReset={onZoomReset}
                              onBucketClick={onErrorBucketClick} />
            </div>
          </MetricPanel>
        </ChartCard>
        <ChartCard title={durTitle}>
          <MetricPanel compact menuOnly title={durTitle} metricQuery={p99Mq}>
            <div style={{ position: 'relative' }}>
              {/* Madde 4 sweep — scoped band panelinde operatör default'u:
                  avg+p50+p95 açık, p99 GİZLİ (SLO latency threshold'u p99'a
                  bağlıysa p99 açık kalır); kullanıcı lejant seçimi
                  localStorage'da kalıcı ve default'u ezer. By-op modda
                  (etiketler operasyon adları) devre dışı. */}
              <MultiLineChart xRange={xRangeSec} series={p99Series ?? []} unit="ms"
                              height={180}
                              deploys={deployMarkers}
                              thresholds={latencyThresholds}
                              regions={problemRegions}
                              syncKey={syncKey}
                              legendStorageKey={opScope ? 'svc-duration-band' : undefined}
                              defaultHidden={opScope
                                ? defaultLatencyHidden(['avg', 'p50', 'p90', 'p95', 'p99'],
                                    { keepP99: !!latencyThresholds })
                                : undefined}
                              compareSeries={p99Prev ?? undefined}
                              compareOffsetNs={compareOffsetNs}
                              onZoom={onZoom}
                              onZoomReset={onZoomReset}
                              onBucketClick={onLatencyBucketClick} />
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
