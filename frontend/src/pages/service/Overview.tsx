import { useId, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import type { Service, TimeRange, SpanMetricSeries, OperationSummary } from '@/lib/types';
import { timeRangeToNs, rangeToSince } from '@/lib/utils';
import { api } from '@/lib/api';
import { entryLatencyDSL } from '@/lib/entrySpans';
import { panelMaxDataPoints, stepForPoints } from '@/lib/chartStep';
import { Tabs } from '@/components/ui';
import { ServiceAnnotationLane } from '@/components/charts/ServiceAnnotationLane';
import { useServiceDeploys, useSLOs } from '@/lib/queries';
import type { ChartThreshold } from '@/lib/chart/overlays';
import { defaultLatencyHidden } from '@/lib/chart/legendVisibility';
import { ChartCard, type ChartLine } from './charts/ChartCard';
import { scopedChartTitle, scopeTitleTip } from './charts/scopeTitle';
import { sumNullableSeries } from './charts/throughputTotal';
import { MetricThroughputNote } from './MetricThroughputNote';
import { metricLatencyComparable, metricLatencyUnitLabel } from './metricLatencyUnit';
import { buildRootOpLines } from './charts/rootOpSeries';
import { useRootOpLatency } from './charts/useRootOpLatency';
import { OpsCard, DbCard } from './OverviewTables';
import { TopEndpointsCard } from './TopEndpointsCard';
import { MetricPanel } from '@/components/MetricPanel';
import { AIAnalysisPanel } from '@/components/AIAnalysisPanel';
import { ServiceNeighbors } from '@/components/ServiceNeighbors';
import { metricQuery, type MetricQuery } from '@/lib/metricQuery';
import { firstNum } from './overviewKpi';

// Service Overview (v0.7.92+) — Dynatrace-style at-a-glance APM view, ported
// from the design handoff. The new tab on /service?name=<svc> (becomes the
// default once complete). Reuses the service-bundle data Service.tsx already
// fetched (info); the RED series for the KPI sparklines + charts
// come from one batched span-metric call here.
//
// v0.8.366 — operator-requested trim: the bottom Instances +
// "Recent problems & events" cards are gone (problems already
// surface via the banner/chips, instances live on /hosts), and the
// flat two-column Neighbors block is replaced by the richer
// ServiceNeighbors panel that used to open the Details tab.

// v0.9.257 — the local SINCE_MAP is gone; rangeToSince() in lib/utils.ts
// replaces it. This copy had drifted from the correct ones in Service.tsx /
// ServiceBacktrace.tsx: it mapped '2d'→'2d' and '7d'→'7d', which Go's
// time.ParseDuration rejects, so the neighbours panel silently rendered the
// endpoint's 1h default for a 2d/7d selection. '30d' was missing entirely
// and fell through the same `?? '1h'` hole.
//
// The window is also capped at 24h now: ServiceNeighbors samples the top-N
// traces out of raw `spans`, and an uncapped 720h GROUP BY trace_id would
// blow max_execution_time on a busy prod service. 24h is already the widest
// window this panel served before (the '24h' preset), so the ceiling adds no
// new worst case — and rangeToSince reports `capped` so the panel SAYS it
// narrowed rather than quietly narrowing.
const NEIGHBORS_CAP_S = 86_400;

interface Props {
  windowNs?: { from: number; to: number };
  service: string;
  range: TimeRange;
  info: Service | null;
  operations: OperationSummary[];
  // v0.9.377 (redesign D1) — bundle'ın giriş-span endpoint slotu.
  endpoints?: import('@/lib/types').EndpointRow[];
  // v0.8.534 — drag-zoom on any Overview chart → parent maps to the global
  // ?range=. Passed down to every ChartCard/OverviewChart (mirrors the
  // sibling Performance/ServiceCharts wiring in Service.tsx).
  onZoom?: (fromSec: number, toSec: number) => void;
  // Grafana-parite M1 — çift-tık: Service.tsx zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
  // v0.9.83 — sorgu penceresi (unix sec): x-ekseni pencereye sabitlenir.
  xRange?: { from: number; to: number } | null;
}

function vals(s?: SpanMetricSeries[] | null): number[] {
  return s && s[0] ? s[0].points.map(p => p.value) : [];
}

// Trend delta vs the prior window — mean of the first third vs the last
// third of the series (mirrors the design's data.js delta()/prior()). >0.5%
// = up, <-0.5% = down, else flat. Returns null when the series is too short.
type Delta = { pct: string; dir: 'up' | 'down' | 'flat' };
function computeDelta(arr: number[]): Delta | null {
  if (arr.length < 6) return null;
  const third = Math.max(1, Math.floor(arr.length / 3));
  const mean = (xs: number[]) => xs.reduce((a, b) => a + b, 0) / (xs.length || 1);
  const prev = mean(arr.slice(0, third));
  const cur = mean(arr.slice(-third));
  if (prev === 0) return null;
  const d = ((cur - prev) / prev) * 100;
  return { pct: Math.abs(d).toFixed(1), dir: d > 0.5 ? 'up' : d < -0.5 ? 'down' : 'flat' };
}

// Full-bleed gradient sparkline pinned to the bottom of a KPI tile. Inline
// SVG (the existing Sparkline pattern), stretched to the tile width via
// preserveAspectRatio="none"; gradient fill 28%→0% of the series colour.
function OvSparkline({ data, color }: { data: number[]; color: string }) {
  const gid = useId();
  if (data.length < 2) return null;
  const W = 120, H = 34, pad = 2;
  const mn = Math.min(...data), mx = Math.max(...data), rng = mx - mn || 1;
  const xs = (i: number) => pad + (i / (data.length - 1)) * (W - pad * 2);
  const ys = (v: number) => H - pad - ((v - mn) / rng) * (H - pad * 2 - 2);
  const line = data.map((v, i) => `${i ? 'L' : 'M'}${xs(i).toFixed(1)},${ys(v).toFixed(1)}`).join(' ');
  const area = `${line} L${xs(data.length - 1).toFixed(1)},${H} L${xs(0).toFixed(1)},${H} Z`;
  return (
    <svg className="ov-spark" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" aria-hidden="true">
      <defs>
        <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.28" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${gid})`} />
      <path d={line} fill="none" stroke={color} strokeWidth="1.6" vectorEffect="non-scaling-stroke"
        strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  );
}

function KpiTile({ lab, val, unit, accent, spark, delta, goodWhenUp, note }: {
  lab: string; val: string; unit?: string; accent: string; spark?: number[];
  delta?: Delta | null; goodWhenUp?: boolean;
  // v0.9.240 — hover text stating WHAT the number is measured over. Latency
  // tiles carry the entry-span scope so the definition isn't folklore.
  note?: string;
}) {
  // Color by whether the move is GOOD for this metric (README §Status
  // semantics): throughput/apdex up = good (green); failure/latency up =
  // bad (red). The .ov-delta classes encode up=err/down=ok by default, with
  // .up.good / .down.bad overrides for the goodWhenUp case.
  const deltaCls = delta
    ? `ov-delta ${delta.dir}${goodWhenUp && delta.dir === 'up' ? ' good' : ''}${goodWhenUp && delta.dir === 'down' ? ' bad' : ''}`
    : '';
  return (
    <div className="card ov-kpi" title={note}>
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">{lab}</div>
      <div className="ov-val">{val}{unit && <span className="ov-unit">{unit}</span>}</div>
      {delta && (
        <div className={deltaCls}>
          {delta.dir === 'up' ? '▲' : delta.dir === 'down' ? '▼' : '—'} {delta.pct}%
          <span style={{ color: 'var(--text3)', fontWeight: 500 }}>vs prior</span>
        </div>
      )}
      {spark && spark.length > 1 && <OvSparkline data={spark} color={accent} />}
    </div>
  );
}

// ChartCard v0.9.87'de charts/ChartCard.tsx'e taşındı (Runtime paneli de kullanır).

export function ServiceOverview({ service, range, windowNs, info, operations, endpoints = [], onZoom, onZoomReset }: Props) {
  // v0.8.480 — üst sayfa pencereyi çözdüyse AYNISI kullanılır: RED
  // prefetch'in RQ anahtarı ancak böyle tutar (timeRangeToNs göreli
  // aralıkta Date.now()'a bağlı, iki ayrı hesap anahtar kaçırır).
  const computed = useMemo(() => timeRangeToNs(range), [range]);
  const { from, to } = windowNs ?? computed;
  // Neighbours window — clock-free, but hoisted so the two props read one
  // computation instead of two calls that could drift apart.
  const nb = rangeToSince(range, NEIGHBORS_CAP_S);
  const windowSec = Math.max(1, (to - from) / 1e9);
  // v0.9.83 — grafiklerin x-ekseni sorgu penceresine sabitlenir (madde 2).
  const xRange = useMemo(() => ({ from: from / 1e9, to: to / 1e9 }), [from, to]);
  // Grafana-parite M1 — ServiceCharts.tsx ile AYNI sync key: Service
  // sayfasındaki TÜM grafikler (Overview RED üçlüsü + Details/Performance)
  // tek crosshair'le birlikte gezinir.
  const chartSync = `service:${service}`;

  // One batched span-metric call: rate + error_rate + p99 + p50 over the
  // same WHERE (service.name = svc). Feeds the KPI sparklines + RED charts.
  // v0.9.391 (Faz B) — mdp: 3 kolonlu panel bütçesi; Service.tsx prefetch
  // AYNI formül + AYNI key'i üretir (parite şart — ölçülmüş ref değil,
  // viewport türevi; gerekçe panelMaxDataPoints yorumunda). select: zarfın
  // series'ini soyar — tüketici gövdeleri değişmeden kalır, stepSeconds
  // gerektiğinde d'den okunur.
  const redMdp = panelMaxDataPoints(3);
  const seriesQ = useQuery({
    queryKey: ['service-overview-red', service, from, to, redMdp],
    queryFn: () => api.spanMetricBatch({
      from, to, maxDataPoints: redMdp,
      dsl: `service.name = "${service.replace(/"/g, '\\"')}"`,
      aggs: [
        { name: 'rate', agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99', agg: 'p99', field: 'duration_ms' },
        { name: 'p95', agg: 'p95', field: 'duration_ms' },
        { name: 'p50', agg: 'p50', field: 'duration_ms' },
      ],
    }),
    select: (d: { stepSeconds: number; series: Record<string, import('@/lib/types').SpanMetricSeries[] | null> }) => d.series,
    enabled: !!service,
    staleTime: 30_000,
  });
  // v0.9.253 — seriesQ is now FALLBACK-ONLY. Every rendered RED number and
  // series reads the entry-scoped query below; this all-span one survives
  // solely to cover a service with no server/consumer spans at all (see
  // usingAllSpans). It keeps the MV fast path (no `kind` filter), so the
  // extra call is the cheap one of the pair. Its `s` / `redStatus` bindings
  // are gone because nothing renders from them any more — reintroducing
  // either is the tell that a panel has drifted back to all-span numbers.

  // v0.9.240 (operatör: "kafka 0.1ms olduğu için medyan hep 1ms çıkıyor") —
  // response-time artık GİRİŞ span'lerinden hesaplanıyor (server + consumer),
  // yani servisin kendi işi; yaptığı DB/HTTP çağrıları değil.
  //
  // v0.9.129 aynı sorunu Kafka'yı çıkararak çözmeye çalışmıştı, ama yetmedi:
  // asıl kütle Kafka değil CLIENT span'leri. Operatörün prod servisinde en
  // yoğun operasyonlar 700K/440K/270K çağrılık SELECT'ler, hepsi 0.2-0.4ms —
  // gerçek istekler bu kalabalığın içinde binde birlik azınlık, medyan da
  // onların değil veritabanı çağrılarının medyanı oluyordu. Kanıt: Kafka
  // çıkarıldığı hâlde P50 hâlâ 0.59ms görünüyordu.
  //
  // consumer BİLEREK dahil — kuyruk mesajı işlemek de servisin kendi işi, ve
  // v0.9.129'un korumaya çalıştığı "kafka-consumer servis sıfır görünmesin"
  // kaygısını doğru şekilde karşılayan yer burası.
  //
  // v0.9.253 (operatör, GENEL İLKE: "giriş spanleri üzerinden hesaplansın
  // aynı dynatrace gibi") — throughput ve error rate de bu sorguya taşındı.
  // v0.9.240'ta bilerek dışarıda bırakılmışlardı çünkü etki büyüktü ve
  // ölçülmeden değiştirilmemeliydi: demo veride rps 1.6 → 0.2 (8× DÜŞER),
  // error rate %2.03 → %9.79 (5× ÇIKAR). İkisi de yanlış değil — servisin
  // KENDİ istekleri sayılınca hata oranı da o istekler üzerinden hesaplanır;
  // eski sayı, on binlerce 0.2ms'lik DB çağrısıyla seyreltilmiş olandı.
  //
  // Ek istek YOK: `kind` filtresi MV fast-path'ini zaten devre dışı bırakıyor
  // (service_summary_5m'de kind boyutu yok), yani bu sorgu hâlihazırda ham
  // span okuyordu. Aynı sorguya iki agg eklemek ek round-trip getirmiyor.
  // v0.9.665 (operatör isteği) — throughput'u METRİKTEN de oku.
  //
  // Overview'ın throughput'u SPAN türevli (giriş-span ilkesi). Bu sorgu
  // ikinci bir kaynak getiriyor: Prometheus biçimli sayaç metriği, servis
  // kimliği `job` etiketinin son bölümünde ("<namespace>/<servis>").
  //
  // Amaç KIYASLAMA: iki çizgi yan yana durunca span sayımı ile metrik
  // sayımının aynı şeyi söyleyip söylemediği görülüyor. Ayrışıyorlarsa
  // bu başlı başına bir bulgu (örnekleme, giriş-span kapsamı, ya da
  // metriğin farklı bir yüzeyi ölçmesi).
  const metricTputQ = useQuery({
    queryKey: ['service-metric-throughput', service, from, to],
    queryFn: () => api.serviceMetricThroughput(service, from, to),
    staleTime: 30_000,
  });

  const latencyQ = useQuery({
    queryKey: ['service-overview-entry-red', service, from, to, redMdp],
    queryFn: () => api.spanMetricBatch({
      from, to, maxDataPoints: redMdp,
      dsl: entryLatencyDSL(service),
      aggs: [
        { name: 'rate', agg: 'rate' },
        { name: 'error_rate', agg: 'error_rate' },
        { name: 'p99', agg: 'p99', field: 'duration_ms' },
        { name: 'p95', agg: 'p95', field: 'duration_ms' },
        { name: 'p50', agg: 'p50', field: 'duration_ms' },
        // Madde 4 sweep (operatör onayı) — latency paneline avg serisi;
        // default görünürlük avg+P50+P95 açık / P99 gizli (aşağıda
        // defaultLatencyHidden), kullanıcı lejant seçimi kalıcı ve ezer.
        { name: 'avg', agg: 'avg', field: 'duration_ms' },
        // v0.9.491 — v0.9.476'nın apdex agg'ı kaldırıldı (operatör:
        // "Service overview'de apdex'e gerek yok"); backend agg yaşıyor,
        // Explore'dan hâlâ sorgulanabilir.
      ],
    }),
    select: (d: { stepSeconds: number; series: Record<string, import('@/lib/types').SpanMetricSeries[] | null> }) => d.series,
    enabled: !!service,
    staleTime: 30_000,
  });
  // v0.9.240 — fallback. A pure batch / producer service emits no server and
  // no consumer spans, so the entry query legitimately returns nothing. Going
  // blank there would trade one wrong number for no number, so we fall back to
  // the all-span series (`s`, already fetched for throughput — no extra
  // request) and SAY SO in the panel instead of quietly changing what the
  // chart means.
  const entryHasData = (latencyQ.data?.p50 ?? []).some(
    ser => (ser.points ?? []).some(p => p.value != null));
  const usingAllSpans = !latencyQ.isLoading && !latencyQ.isError && !entryHasData;
  const lat = usingAllSpans ? seriesQ.data : latencyQ.data;
  const latStatus: 'loading' | 'error' | 'ready' =
    latencyQ.isLoading ? 'loading' : latencyQ.isError ? 'error' : 'ready';
  // What the RED panel is actually measuring — the KPI tiles, the scope badge
  // and (v0.9.483) the chart titles all read this ONE string. v0.9.253: it
  // describes throughput and failure rate too, not just latency; all three
  // read the same series. v0.9.483 — metin charts/scopeTitle.ts'e taşındı:
  // başlık eki ve açıklama tek yerden gelsin (ikisi ayrı yazılınca biri
  // güncellenip diğeri bayatlıyordu).
  const latScopeNote = scopeTitleTip(usingAllSpans);

  // ── Response time · operasyon kırılımı (v0.9.484, operatör onayı: "root
  // spanler için multichart") ────────────────────────────────────────────
  //
  // Seçim URL'de (?rtops=1): ev kuralı — ekranda ne görüldüğünü değiştiren
  // her operatör seçimi kopyalanabilir linke biner. Yabancı parametreler
  // korunur (prev kopyası), replace:true (geri tuşu görünüm değiştirmekle
  // dolmaz). Service.tsx zaten kendi parametrelerini setSearchParams(prev)
  // ile yazıyor — bu sayfada `prev` bayat bir alt küme DEĞİL, o yüzden düz
  // prev formu yeterli (ham replaceState yazan sayfalarda olmaz).
  const [searchParams, setSearchParams] = useSearchParams();
  const splitByOp = searchParams.get('rtops') === '1';
  const setSplitByOp = (next: boolean) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (next) p.set('rtops', '1'); else p.delete('rtops');
    return p;
  }, { replace: true });

  // Toplam görünüm batch'i maxDataPoints ile, /api/spans/metric ise step
  // (saniye) ile konuşuyor. Aynı pencereyi aynı çözünürlükte görmek için
  // nokta bütçesi rung'a çevrilir — görünüm değiştirince bucket boyu
  // değişip grafik zıplamaz.
  const opsStep = useMemo(
    () => stepForPoints(Math.max(1, (to - from) / 1e9), redMdp),
    [from, to, redMdp]);
  // İstek YALNIZ kırılım açıkken (ES/CH maliyet disiplini — varsayılan bedava).
  const opsQ = useRootOpLatency(service, from, to, opsStep, splitByOp);
  // Saf projeksiyon: alan bazlı ilk 5, union zaman ekseni, "+N daha" notu.
  const opsView = useMemo(() => buildRootOpLines(opsQ.data), [opsQ.data]);
  const opsStatus: 'loading' | 'error' | 'ready' =
    opsQ.isLoading ? 'loading' : opsQ.isError ? 'error' : 'ready';
  // Fallback (usingAllSpans) durumunda kırılım YAPISAL olarak boş: kırılım
  // giriş span'lerinden okur, o servisin hiç giriş span'i yok. ChartCard'ın
  // jenerik "No data in this window"u burada yalan söylerdi ("pencere boş"
  // değil, "bu servis bu kırılımı üretemez"), o yüzden sebebi SÖYLENİR.
  const opsNote = usingAllSpans
    ? 'Bu serviste giriş span’i (server/consumer) yok — operasyon kırılımı boş.'
    : opsView.note;
  const rtSegment = (
    <Tabs variant="segmented" ariaLabel="Response time görünümü"
      value={splitByOp ? 'ops' : 'agg'}
      onChange={k => setSplitByOp(k === 'ops')}
      items={[
        { key: 'agg', label: 'Toplam', hint: 'Servisin giriş span’leri — avg / P50 / P95 / P99' },
        { key: 'ops', label: 'Operasyonlar', hint: 'En yüksek P95 alanına sahip 5 giriş operasyonu, her biri kendi P95 çizgisi' },
      ]} />
  );

  // Grafana-parite M3 — failure-rate paneline SLO hata-bütçesi eşiği.
  // ServiceCharts'ın error-rate threshold KAYNAĞININ aynısı (useSLOs →
  // availability SLO → (1-target)·100), OverviewChart'ın yeni thresholds
  // prop'uyla çizilir; alert eşiğiyle grafik arasında görsel bağ kurulur.
  // useSLOs RQ-dedupe'lu — Performance sekmesindeki ServiceCharts ile aynı
  // sorguyu paylaşır, ek yük yok.
  const slosQ = useSLOs();
  const failureThresholds = useMemo<ChartThreshold[] | undefined>(() => {
    const t: ChartThreshold[] = [];
    for (const slo of slosQ.data ?? []) {
      if (slo.service !== service || slo.sliType !== 'availability') continue;
      const errBudgetPct = (1 - slo.target) * 100;
      const opSuffix = slo.operation ? ` (${slo.operation})` : '';
      t.push({
        value: errBudgetPct,
        label: `err ≤ ${errBudgetPct.toFixed(2)}%${opSuffix}`,
        color: 'var(--err)',
      });
    }
    return t.length > 0 ? t : undefined;
  }, [slosQ.data, service]);

  const deploysQ = useServiceDeploys(service, from, to);
  // The single deploy marker drawn on the charts = the latest deploy inside
  // the window (the design shows one ▼ flag).
  const deploy = useMemo(() => {
    const ds = (deploysQ.data ?? []).filter(d => d.timeUnixNs >= from && d.timeUnixNs <= to);
    if (!ds.length) return null;
    const latest = ds.reduce((a, b) => (b.timeUnixNs > a.timeUnixNs ? b : a));
    return { sec: latest.timeUnixNs / 1e9, label: latest.version };
  }, [deploysQ.data, from, to]);

  // Throughput series (OK vs Errors) derived from the MV-backed rate +
  // error_rate series — no extra query, no raw-spans scan (invariant #3).
  // Errors = rate × err%, OK = the remainder; the two add up to the total rate.
  // (A 4xx-vs-5xx split would need an HTTP-status MV dimension.)
  //
  // v0.9.483 (operatör: yığılmış alan sorgulandı) — kart artık ÇİZGİ modunda
  // ve iki çizgi taşıyor: "Toplam" (var(--accent)) + "Errors" (var(--err)).
  // Yığın kalkınca toplam görsel olarak okunamaz oldu, bu yüzden VERİ olarak
  // çiziliyor (sumNullableSeries — boşluk semantiği testli).
  //   chart → çizilen çizgiler   (Toplam, Errors)
  //   stats → alttaki tablo      (OK, Errors + tfoot "Toplam") — v0.9.103'ten
  //           beri ne gösteriyorsa aynısı; Toplam'ı satır olarak da koysaydık
  //           tfoot toplamı trafiği iki katı gösterirdi.
  const throughput = useMemo<{ chart: ChartLine[]; stats: ChartLine[] }>(() => {
    // v0.9.253 — `lat` is the entry-scoped series when the service has entry
    // spans, and the all-span series when it doesn't (usingAllSpans). Reading
    // through it keeps throughput, error rate and latency on ONE population:
    // a chart showing entry-span latency above all-span throughput would be
    // two different services stacked on one card.
    const ratePts = lat?.rate?.[0]?.points ?? [];
    const erPts = lat?.error_rate?.[0]?.points ?? [];
    if (ratePts.length < 2) {
      // Tek nokta / veri yok — ayrıştıracak bir şey yok, ham hız çizilir.
      const only: ChartLine[] = [{ series: lat?.rate ?? [], color: 'var(--accent)', label: 'Toplam' }];
      return { chart: only, stats: only };
    }
    const okPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
    const errPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
    const okLine: ChartLine = { series: [{ groupKey: [], points: okPts }], color: 'var(--ok)', label: 'OK' };
    const errLine: ChartLine = { series: [{ groupKey: [], points: errPts }], color: 'var(--err)', label: 'Errors' };
    // Toplam = OK + Errors, eleman-eleman (boşluklar korunur). series boş:
    // x ekseni Errors çizgisinden gelir — ikisi de ratePts'ten türediği için
    // aynı bucket kümesi, index kayması yok (ChartCard hizalama sözleşmesi).
    const totalLine: ChartLine = {
      series: [], color: 'var(--accent)', label: 'Toplam',
      values: sumNullableSeries(okPts.map(p => p.value), errPts.map(p => p.value)),
    };
    return { chart: [totalLine, errLine], stats: [okLine, errLine] };
  }, [lat]);

  // Metrik türevli çizgi, span türevli olanın YANINA. Yerine geçmiyor:
  // hangisinin doğru olduğuna operatör bakarak karar versin — sessizce
  // kaynak değiştirmek, grafiğin ne anlattığını belirsizleştirirdi.
  const metricTputLine = useMemo<ChartLine | null>(() => {
    const d = metricTputQ.data;
    if (!d?.series || d.series.length === 0) return null;
    // v0.9.675 — etiket KAYNAĞI söylüyor. Sabit "Metrik (job)" yazıyordu,
    // oysa v0.9.671'den beri eşleşme job / service / name / service_name
    // kolonundan gelebiliyor. `name`den eşleşen bir çizgiyi "(job)" diye
    // etiketlemek lejantı yalancı yapardı — ve bu çizginin TEK işi span
    // türevli sayımla kıyaslanmak, yani neyi ölçtüğü okunabilir olmalı.
    return {
      series: d.series,
      color: 'var(--teal)',
      label: `Metrik · ${d.matchedBy ?? '?'}`,
    };
  }, [metricTputQ.data]);

  // v0.9.676 (operatör: "response time için de bir panel yapabilir
  // misin") — metrik türevli gecikme. Aynı uçtan, THROUGHPUT'UN BULDUĞU
  // seriden geliyor: iki panelin farklı serilere bakması kıyaslamayı
  // sessizce anlamsız kılardı.
  const metricLatLines = useMemo<ChartLine[]>(() => {
    const l = metricTputQ.data?.latency;
    if (!l) return [];
    const out: ChartLine[] = [];
    if (l.p50) out.push({ series: l.p50, color: 'var(--purple)', label: 'P50' });
    if (l.p95) out.push({ series: l.p95, color: 'var(--orange)', label: 'P95' });
    if (l.p99) out.push({ series: l.p99, color: 'var(--err)', label: 'P99' });
    return out;
  }, [metricTputQ.data]);

  // v0.9.170 (operatör-bildirimi: cluster çözülemeyen / metrik-yoğun
  // servislerde "bütün Service Overview boş"). Service-summary bundle (info)
  // null olsa da Overview BLANK dönmez — headline sayılar RED/latency
  // batch'inden türetilir (service.name üzerinden, cluster-BAĞIMSIZ); info
  // yalnız fallback. Böylece service_summary_5m'de satırı olmayan bir servis
  // bile veri varsa dolar, hiç yoksa boş-durum gösterir — asla tümden
  // boşalmaz. (Eski davranış: `if (!info) return null` → komple blank.)
  const rateNow = vals(lat?.rate).slice(-1)[0];
  const errNow = vals(lat?.error_rate).slice(-1)[0];
  const p99Now = vals(lat?.p99).slice(-1)[0];
  // v0.9.483 — p50Now / p50Ms kaldırıldı: tek tüketicileri "Response time ·
  // median" karosuydu (operatör: "bence response time mediana da gerek yok").
  // lat.p50 SERİSİ duruyor — Response time grafiğinin lejant-kontrollü çizgisi.
  // v0.9.253 — ENTRY series first, `info` only as the fallback. `info` comes
  // from service_summary_5m, which has no `kind` dimension, so it can only
  // ever report the all-span number. Preferring it would have left the tiles
  // saying one thing while the chart under them said another.
  //
  // KNOWN DIVERGENCE, deliberate and temporary: /services and the SLO
  // availability path still read that MV, so their numbers stay all-span
  // until the MV gains entry_count_state / entry_error_count_state columns.
  // See feedback-entry-span-principle.
  const rps = firstNum(rateNow, info ? info.spanCount / windowSec : undefined);
  const errorRatePct = firstNum(errNow, info ? info.errorRate : undefined);
  const p99Ms = firstNum(p99Now, info?.p99DurationMs);

  // "Every metric is a doorway" (Phase C) — canonical descriptors for each KPI
  // + RED chart. The SAME object that the panel carries is what the Explorer
  // re-opens via MetricPanel's ⋮ / body-click / `e`. filters ALWAYS pin the
  // focused service; KPI tiles use viz:'stat', RED charts use viz:'line'. The
  // descriptor only feeds the doorway — it does NOT drive the rendered numbers
  // (those stay the existing info.* / span-metric series, byte-identical).
  const svcFilter = { 'service.name': service };
  const mkThroughput = (viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'calls_total', agg: 'rate', unit: 'rps', filters: svcFilter, viz, range });
  const mkFailureRate = (viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'calls_total', agg: 'error_rate', unit: '%', filters: svcFilter, viz, range });
  const mkLatency = (agg: 'p50' | 'p95' | 'p99', viz: MetricQuery['viz']) =>
    metricQuery({ metric: 'duration_milliseconds_bucket', agg, unit: 'ms', filters: svcFilter, viz, range });
  // v0.9.491 (operatör: "Service overview'de apdex'e gerek yok") — v0.9.476
  // Apdex karosu + grafiği kaldırıldı. Backend apdex agg'ı ve MV state'leri
  // yaşıyor; Explore'dan calls_total/apdex hâlâ sorgulanabilir.

  return (
    <div style={{ marginTop: 4 }}>
      {/* v0.9.378 (redesign D2, mockup af7419e5) — Overview'a Details'ın
          dtl-sech bölüm dili geldi: Altın sinyaller / Giriş noktaları /
          Bağlam. Kapsam tanımı tooltip folklorundan çıkıp TEK görünür
          rozete indi — beş karo ve üç grafik aynı rozete bakar; fallback
          amber'e döner. */}
      <div className="dtl-sech">Altın sinyaller
        <span className={`badge ${usingAllSpans ? 'b-warn' : 'b-gray'}`}
          style={{ textTransform: 'none', letterSpacing: 0 }} title={latScopeNote}>
          {usingAllSpans ? 'kapsam: tüm span\u2019ler' : 'kapsam: giriş span\u2019leri'}
        </span>
      </div>
      {/* KPI row — golden signals + full-bleed trend sparklines. Each tile is
          wrapped in the reusable MetricPanel doorway (compact: a hover-revealed
          ⋮ + body-click → Explore); the tile body renders verbatim. */}
      <div className="ov-grid ov-kpis ov-mb">
        <MetricPanel compact menuOnly title="Throughput" metricQuery={mkThroughput('stat')}>
          <KpiTile lab="Throughput" val={rps.toFixed(rps < 10 ? 1 : 0)} unit=" req/s" accent="var(--accent)" spark={vals(lat?.rate)} delta={computeDelta(vals(lat?.rate))} goodWhenUp note={latScopeNote} />
        </MetricPanel>
        {/* v0.9.631 (operatör: "failure rate yüzdesi grafiğin üzerinde olsun,
            p99 ile yer değişsin") — Failure rate karosu SON sıraya alındı,
            böylece altındaki RED grafik şeridinin ÜÇÜNCÜ grafiği (Failure
            rate) ile aynı kolona düşüyor: yüzde, ait olduğu eğrinin tam
            üstünde okunuyor. */}
        <MetricPanel compact title="Response time · P99" metricQuery={mkLatency('p99', 'stat')}>
          <KpiTile lab="Response time · P99" val={p99Ms.toFixed(0)} unit=" ms" accent="var(--orange)" spark={vals(lat?.p99)} delta={computeDelta(vals(lat?.p99))} goodWhenUp={false} note={latScopeNote} />
        </MetricPanel>
        <MetricPanel compact menuOnly title="Failure rate" metricQuery={mkFailureRate('stat')}>
          <KpiTile lab="Failure rate" val={`${errorRatePct.toFixed(2)}%`} accent="var(--err)" spark={vals(lat?.error_rate)} delta={computeDelta(vals(lat?.error_rate))} goodWhenUp={false} note={latScopeNote} />
        </MetricPanel>
        {/* v0.9.483 (operatör: "bence response time mediana da gerek yok") —
            "Response time · median" karosu kaldırıldı; P99 karo olarak kalıyor.
            P50 SERİSİ duruyor: Response time grafiğinde lejant-kontrollü bir
            çizgi (varsayılan açık) — kaldırılan yalnız KPI şeridindeki karo. */}
        {/* v0.9.491 — Apdex karosu kaldırıldı (operatör: "Service overview'de
            apdex'e gerek yok"); .ov-kpis 3 kolona indi. */}
      </div>

      {/* RED charts row — response time / throughput / failure rate, each
          with the deploy markers from the service bundle. Each chart carries
          its viz:'line' descriptor through the compact MetricPanel doorway.
          v0.9.362 — menuOnly: bu grafikler drag-zoom + tooltip-pin + lejant
          isolate sahibi; gövde-tıklaması /explore'a giderse drag'ın kuyruk
          tıklaması sayfayı savurur ve pin/isolate hiç ulaşılamaz olurdu.
          ServiceCharts üç panelinde bu prop'u zaten geçiyor (oradaki yorum
          sebebi yazar); Overview'da unutulmuştu — operatör bunu "grafikler
          bozuk/zıplıyor" diye yaşıyordu. Doorway hover ⋮ ve `e` kısayoluyla
          erişilebilir kalıyor; KPI karoları gövde-tıklamasını koruyor. */}
      <div className="ov-grid ov-charts-3 ov-mb">
        <MetricPanel compact menuOnly title="Response time" metricQuery={mkLatency('p99', 'line')}>
          {/* v0.9.484 — TEK kart, iki görünüm. Başlık/kapsam tooltip'i
              (v0.9.483) ve deploy ▼ / eşik / zoom / sync kablolaması ikisinde
              de AYNI; değişen yalnız çizgiler, durum ve lejant anahtarı.
              legendStorageKey AYRI olmak ZORUNDA: "P95" etiketi iki görünümde
              de var, ortak anahtarla toplam görünümde gizlenen bir çizgi
              kırılımda da gizli gelirdi (lejant seçimi çapraz zehirlenmesi).
              key= de şart: aynı konumda aynı bileşen tipi olduğu için React
              örneği YENİDEN KULLANIRDI ve uPlot örneği bir görünümün seri
              kümesiyle kurulmuşken diğerinin verisini alırdı (lejant
              görünürlüğü afterBuild'de okunuyor). Ayrı key = temiz kurulum. */}
          {splitByOp ? (
            <ChartCard key="rt-ops" title={scopedChartTitle('Response time', usingAllSpans)} titleTip={latScopeNote} unit=" ms" mode="line" deploy={deploy} status={opsStatus} onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync} xRange={xRange}
              headerAside={rtSegment} note={opsNote}
              legendStorageKey="ov-response-time-ops" statsDefaultCollapsed
              lines={opsView.lines} />
          ) : (
            <ChartCard key="rt-agg" title={scopedChartTitle('Response time', usingAllSpans)} titleTip={latScopeNote} unit=" ms" mode="line" deploy={deploy} status={latStatus} onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync} xRange={xRange}
              headerAside={rtSegment}
              legendStorageKey="ov-response-time" statsDefaultCollapsed
              defaultHidden={defaultLatencyHidden(['avg', 'P50', 'P95', 'P99'])}
              lines={[
              { series: lat?.avg ?? [], color: 'var(--teal)', label: 'avg' },
              { series: lat?.p50 ?? [], color: 'var(--purple)', label: 'P50' },
              { series: lat?.p95 ?? [], color: 'var(--orange)', label: 'P95' },
              { series: lat?.p99 ?? [], color: 'var(--err)', label: 'P99' },
            ]} />
          )}
          {/* v0.9.676 — metrik türevli gecikme KENDİ kartında, span
              türevlinin ALTINDA (throughput'takiyle aynı düzen).
              Yüzdelikler histogram KOVA SINIRLARINDAN; bir sayaçta
              gecikme diye bir şey olmadığı için yalnız histogramda
              çiziliyor. */}
          {metricLatLines.length > 0 && (
            <div style={{ marginTop: 10 }}>
              <ChartCard
                title={`Response time · metrik (${metricTputQ.data?.metric ?? ''})`}
                titleTip={`Kaynak: ${metricTputQ.data?.metric ?? '?'} · histogram kovalarından · eşleşme ${metricTputQ.data?.matchedBy ?? '?'}${metricTputQ.data?.latencyUnitKnown === false ? ' · BİRİM TANINMADI, ölçeklenmedi' : ''}`}
                unit={metricLatencyUnitLabel(metricTputQ.data?.latencyUnitKnown, metricTputQ.data?.latencyUnit)}
                mode="line"
                deploy={deploy} onZoom={onZoom} onZoomReset={onZoomReset}
                syncKey={chartSync} xRange={xRange}
                legendStorageKey="ov-response-time-metric" statsDefaultCollapsed
                lines={metricLatLines} />
              {!metricLatencyComparable(metricTputQ.data?.latencyUnitKnown) && (
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4 }}>
                  Metriğin birimi tanınmadı (<code>{metricTputQ.data?.latencyUnit || 'boş'}</code>)
                  — değerler ms'ye ÇEVRİLMEDİ, üstteki panelle doğrudan
                  kıyaslanamaz.
                </div>
              )}
            </div>
          )}
        </MetricPanel>
        <MetricPanel compact title="Throughput" metricQuery={mkThroughput('line')}>
          {/* v0.9.253 — status ve seri artık ENTRY sorgusundan. Kart üstündeki
              KPI giriş span'lerini sayarken altındaki grafiğin tüm span'leri
              çizmesi, aynı kartta iki farklı servisi üst üste koymak olurdu. */}
          <ChartCard title={scopedChartTitle('Throughput', usingAllSpans)} titleTip={latScopeNote} unit=" req/s" mode="line" deploy={deploy} status={latStatus} onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync} xRange={xRange}
            legendStorageKey="ov-throughput" statsDefaultCollapsed
            lines={throughput.chart} statsLines={throughput.stats} />
          {/* v0.9.665 — TANILAMA. Boş bir grafik "metrik yok" ile "desen
              tutmadı"yı aynı gösterir; ikisi bambaşka eylem gerektiriyor
              (collector'ı düzelt / deseni düzelt). Bu yüzden neden
              yazılıyor, gerçek `job` değerleriyle birlikte. */}
          {/* v0.9.675 (operatör: "Throughput'un altında ayrı bir panel
              olsun") — metrik türevli seri KENDİ kartında.
              Önce span türevli çizginin yanına konmuştu; aynı eksende iki
              farklı ölçüm yöntemi üst üste binince hangisinin ne olduğu
              okunmuyordu. Ayrı kart ikisini kıyaslanabilir tutuyor:
              aynı pencere, aynı birim, ayrı eksen. */}
          {metricTputLine && (
            <div style={{ marginTop: 10 }}>
              <ChartCard
                title={`Throughput · metrik (${metricTputQ.data?.metric ?? ''})`}
                titleTip={`Kaynak: ${metricTputQ.data?.metric ?? '?'} · instrument ${metricTputQ.data?.instrument ?? '?'} · eşleşme ${metricTputQ.data?.matchedBy ?? '?'}. Span türevli throughput'tan BAĞIMSIZ bir ölçüm — ayrışıyorlarsa bu başlı başına bir bulgu.`}
                unit=" req/s" mode="line"
                deploy={deploy} onZoom={onZoom} onZoomReset={onZoomReset}
                syncKey={chartSync} xRange={xRange}
                legendStorageKey="ov-throughput-metric" statsDefaultCollapsed
                lines={[metricTputLine]} />
            </div>
          )}
          {metricTputQ.data && !metricTputLine && (
            <MetricThroughputNote d={metricTputQ.data} />
          )}
        </MetricPanel>
        <MetricPanel compact title="Failure rate" metricQuery={mkFailureRate('line')}>
          <ChartCard title={scopedChartTitle('Failure rate', usingAllSpans)} titleTip={latScopeNote} unit="%" mode="area" deploy={deploy} status={latStatus} onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync} xRange={xRange}
            legendStorageKey="ov-failure-rate" statsDefaultCollapsed
            thresholds={failureThresholds} lines={[
            { series: lat?.error_rate ?? [], color: 'var(--err)', label: 'errors' },
          ]} />
        </MetricPanel>
        {/* v0.9.491 — v0.9.476 Apdex grafiği kaldırıldı (operatör kararı);
            RED üçlüsü ov-charts-3'ü tam dolduruyor. */}
      </div>

      {/* v0.9.397 (Ş3 yayılım, "sırayla devam") — annotation şeridi
          Overview'da da: üç RED grafiği aynı x-ekseni paylaşıyor, grid'in
          altında TEK şerit. Details ile AYNI bileşen + queryKey (sekmeler
          arası cache paylaşımı). Chart-içi deploy ▼ çizgileri ŞİMDİLİK
          duruyor — emeklilikleri operatör pilotu canlıda görünce. */}
      {onZoom && (
        <ServiceAnnotationLane service={service} fromNs={from} toNs={to}
          onZoomTo={onZoom} />
      )}

      {/* v0.9.139 — dil-runtime grafikleri (JVM/.NET/Go) Overview'dan "Pods"
          sekmesine taşındı (ServicePodsTab, v0.9.158'de yeniden adlandırıldı).
          Operatör talebi. */}

      {/* v0.9.378 (D2) — tablolar Bağlam'ın ÜSTÜNE taşındı (mockup düzeni):
          AI/Neighbors açılınca altındaki tabloların zıplaması sorunu,
          rezerv yükseklik yerine SIRAYLA çözülür — genişleyen paneller
          artık sayfanın sonunda, altlarında itilecek içerik yok. */}
      <div className="dtl-sech">Giriş noktaları</div>
      {/* Top endpoints (giriş span'leri, v0.9.377 D1) + Top DB statements.
          endpoints boşsa (giriş span'i olmayan servis / eski backend) eski
          Operations kartına düş — görünmez-düşme yerine zarif fallback;
          OpsCard dosyasıyla birlikte yaşamaya devam eder (kolay revert). */}
      <div className="ov-grid ov-cols-2 ov-mb">
        {endpoints.length > 0
          ? <TopEndpointsCard service={service} range={range} endpoints={endpoints} />
          : <OpsCard service={service} range={range} operations={operations} />}
        <DbCard service={service} range={range} from={from} to={to} />
      </div>

      <div className="dtl-sech">Bağlam</div>
      {/* Upstream / downstream neighbours — the richer panel that used
          to open the Details tab, moved here v0.8.366 (operator: the
          Details version "daha güzel gösteriyor"); the flat two-column
          Neighbors block it replaces is gone. Full graph on /topology. */}
      <ServiceNeighbors service={service} since={nb.since} capped={nb.capped} defaultOpen />

      {/* AI Analizi — auto-sends this service + selected window (v0.8.89). */}
      <AIAnalysisPanel service={service} rangeS={Math.round((to - from) / 1e9)} />

    </div>
  );
}
