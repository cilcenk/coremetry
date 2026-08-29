import { useId, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import type { Service, TimeRange, SpanMetricSeries, OperationSummary, AnomalyEvent } from '@/lib/types';
import { timeRangeToNs, rangeToSince } from '@/lib/utils';
import { api } from '@/lib/api';
import { entryLatencyDSL, envDSL } from '@/lib/entrySpans';
import { panelMaxDataPoints, stepForWidth } from '@/lib/chartStep';
import { encodeFilters } from '@/lib/urlState';
import { useServiceDeploys, useAnomalyEvents, useAnomalySilences, useCreateAnomalySilence } from '@/lib/queries';
import { windowAnomalies, anomalyRegions, silencedSet, silenceKey } from '@/lib/anomalyRegions';
import { AnomalyWindowTable } from '@/features/anomalies/AnomalyWindowTable';
// v0.10.162 — çekmece TEMBEL: CopilotExplain/RootCauseRibbon/LogsHistogram'ı en sıcak sayfanın chunk'ına sokmasın (yalnız satır tıkında yüklenir).
const AnomalyDetailDrawerLazy = lazy(() => import('@/features/anomalies/AnomalyDetailDrawer').then(m => ({ default: m.AnomalyDetailDrawer })));
import { useAuth } from '@/components/AuthProvider';
import { PanelTitle } from '@/components/ui/PanelTitle';
// ChartLine — throughput memo'sunun taşıyıcı şekli. TİP-ONLY import:
// ChartCard BİLEŞENİ v0.9.844'te bu sayfadan çıktı (eski motor söküldü),
// dosyası ise yaşıyor (Runtime paneli tüketiyor).
import { type ChartLine } from './charts/ChartCard';
import { scopedChartTitle, scopeTitleTip } from './charts/scopeTitle';
import { sumSeries } from './charts/throughputTotal';
import { MetricThroughputNote } from './MetricThroughputNote';
// v0.9.708 — CorePanel LAZY: @grafana/* vendor ağırlığı (~126 KB gzip)
// yalnız panel gerçekten çizilirken iner. Statik import her sayfanın
// vendor chunk'ını 35 KB→1 MB yapıyordu — ölçüldü.
import { lazy, Suspense } from 'react';
import { Spinner } from '@/components/Spinner';
const CorePanelMultiLazy = lazy(() =>
  import('@/components/chart/corePanelEntry').then(m => ({ default: m.CorePanelMulti })));
import {
  topRoutesByArea, metricUnitToGrafana, ROUTE_TOP_N, metricAvgToMs,
} from './charts/routeSeries';
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
  // env(a), v0.9.1041 — global Topbar picker. Narrows BOTH the span RED
  // (seriesQ/latencyQ DSL) AND the metric-derived Response time
  // (metricQueryFull filters) + Throughput (serviceMetricThroughput) so
  // the five headline tiles + three RED charts win env together — never
  // some env-scoped and some all-env (the ikili-hâl the brief forbids).
  env?: string;
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

function KpiTile({ lab, val, unit, accent, spark, delta, goodWhenUp, note, sub }: {
  lab: string; val: string; unit?: string; accent: string; spark?: number[];
  delta?: Delta | null; goodWhenUp?: boolean;
  // v0.9.240 — hover text stating WHAT the number is measured over. Latency
  // tiles carry the entry-span scope so the definition isn't folklore.
  note?: string;
  // v0.9.798 — İKİNCİL SATIR: karonun büyük sayısıyla AYNI kavramın
  // başka bir kaynaktan/agg'den okunmuş hâli. Bugünkü tek tüketici
  // "P99 (span)" — Response time karosu metrik AVG'ye geçince kuyruk
  // görünürlüğü kaybolmasın diye (metrikten P99 prod'da VERİLEMEZ:
  // bucket_bounds yok, v0.9.774'ün kökü).
  //
  // Ayrı ve küçük bir prop olması bilinçli: bu satır tek commit'le geri
  // alınabilir olmalı — kaldırıldığında karo bugünkü hâline döner.
  sub?: string;
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
      {sub && (
        <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2, lineHeight: 1.35 }}>
          {sub}
        </div>
      )}
      {spark && spark.length > 1 && <OvSparkline data={spark} color={accent} />}
    </div>
  );
}

// ChartCard v0.9.87'de charts/ChartCard.tsx'e taşındı (Runtime paneli de kullanır).

export function ServiceOverview({ service, range, windowNs, info, operations, endpoints = [], onZoom, onZoomReset, env = '' }: Props) {
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
    // v0.9.723 — 180s rate penceresi (Grafana [3m] paritesi).
    // v0.9.844 — motor bayrağı anahtardan ÇIKTI, pencere SABİT: tek mod
    // kaldı. Service.tsx'in prefetch'i AYNI commit'te aynı şekli aldı
    // (ayrı düşerse prefetch ölür — v0.9.723 review bulgusu).
    queryKey: ['service-overview-red', service, from, to, redMdp, env],
    queryFn: () => api.spanMetricBatch({
      from, to, maxDataPoints: redMdp,
      rateWindow: 180,
      dsl: `service.name = "${service.replace(/"/g, '\\"')}"` + envDSL(env),
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
    // v0.9.706 — redMdp anahtarda VE istekte: metrik paneli artık kardeş
    // RED grafikleriyle aynı nokta bütçesini geçiyor (px pilotu). Sunucu
    // desteği v0.9.105'ten beri hazırdı; geçiren ilk yüzey bu.
    // v0.9.718 — route kırılımı + 180s rate penceresi (Grafana
    // by(http_route) + [3m] referansı).
    // v0.9.844 — bayrak anahtardan çıktı, breakdown='route' sabitlendi.
    queryKey: ['service-metric-throughput', service, from, to, redMdp, env],
    queryFn: () => api.serviceMetricThroughput(service, from, to, undefined, redMdp,
      { breakdown: 'route', rateWindow: 180, env: env || undefined }),
    staleTime: 30_000,
  });

  const latencyQ = useQuery({
    queryKey: ['service-overview-entry-red', service, from, to, redMdp, env],
    queryFn: () => api.spanMetricBatch({
      from, to, maxDataPoints: redMdp,
      rateWindow: 180,
      dsl: entryLatencyDSL(service) + envDSL(env),
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

  // v0.9.844 — v0.9.484'ün "Response time · operasyon kırılımı" görünümü ve
  // onu taşıyan ?rtops URL parametresi SİLİNDİ (v0.9.736 operatör düzeninin
  // gecikmiş kod karşılığı). URL'den bir seçim kalkarken tek risk eski bir
  // kayıtlı link: ?rtops=1 taşıyan bir URL artık YOK SAYILIR (parametre
  // okunmuyor), sayfa metrik panelini açar — kırık ekran değil, farklı
  // panel.
  const [searchParams, setSearchParams] = useSearchParams();
  // v0.9.742 (operatör tercihi) — metrik paneline tık Metrics sayfasına
  // götürür (tam ekran yerine); mevcut ?range korunur.
  const navigate = useNavigate();
  const metricsHref = (opts?: { by?: string }) => {
    const m = metricTputQ.data?.metric ?? '';
    const r = searchParams.get('range');
    return `/metrics?metric=${encodeURIComponent(m)}&service=${encodeURIComponent(service)}`
      + (opts?.by ? `&by=${encodeURIComponent(opts.by)}` : '')
      + (r ? `&range=${encodeURIComponent(r)}` : '');
  };
  // v0.9.844 — kırılım sorgusu (useRootOpLatency + opsStep + buildRootOpLines
  // projeksiyonu) ve SLO hata-bütçesi eşikleri (useSLOs → failureThresholds)
  // de bu panellerle birlikte gitti. Eşik çizgisi % eksenine aitti; kalan
  // Failure rate paneli req/s çiziyor, yani eşiği ORADA çizmek yanlış eksene
  // yatay çizgi koymak olurdu (geri isteniyorsa % eksenli ayrı panel dilimi).

  const deploysQ = useServiceDeploys(service, from, to);
  // The single deploy marker drawn on the charts = the latest deploy inside
  // the window (the design shows one ▼ flag).
  const deploy = useMemo(() => {
    const ds = (deploysQ.data ?? []).filter(d => d.timeUnixNs >= from && d.timeUnixNs <= to);
    if (!ds.length) return null;
    const latest = ds.reduce((a, b) => (b.timeUnixNs > a.timeUnixNs ? b : a));
    return { sec: latest.timeUnixNs / 1e9, label: latest.version };
  }, [deploysQ.data, from, to]);

  // v0.9.720 — v2 panellerinin deploy işareti: anlık olay → ince bant
  // (pencerenin %0.4'ü; clampRegion sıfır-genişliği eler, o yüzden
  // genişlik ŞART). drawTimeRegions zoom'la doğru konumlanır.
  const deployRegions = useMemo(() => {
    if (!deploy) return undefined;
    const spanSec = Math.max(1, (to - from) / 1e9);
    return [{
      fromSec: deploy.sec, toSec: deploy.sec + spanSec * 0.004,
      color: 'var(--purple)', label: `▼ ${deploy.label}`,
    }];
  }, [deploy, from, to]);

  // v0.10.162 — ANOMALİ bantları aynı `regions` kanalından (etüt seçenek A,
  // dilim 1: sıfır backend). /api/anomalies/events (son 24 saat, en yeni
  // 200; servis süzgeci istemcide) + susturmalar; bant [startedAt, lastSeen],
  // tür rengi (lib/anomalyRegions.ts), sessiz → soluk. Deploy bandı önce,
  // anomali bantları sonra (çakışan bantlar bileşik boyanır; lane/hit-test
  // dilim 2). Altındaki tablo (AnomalyWindowTable) çakışmayı satır satır
  // ayırt eder; satır → mevcut AnomalyDetailDrawer (?anomaly=, replace:true).
  const anomaliesQ = useAnomalyEvents();
  const silencesQ = useAnomalySilences();
  const createSilence = useCreateAnomalySilence();
  const { user } = useAuth();
  const canEditAnomaly = user?.role === 'admin' || user?.role === 'editor';
  const windowEvents = useMemo(() => windowAnomalies(anomaliesQ.data?.items, service, from, to), [anomaliesQ.data, service, from, to]);
  const silenced = useMemo(() => silencedSet(silencesQ.data), [silencesQ.data]);
  const chartRegions = useMemo(() => {
    const a = anomalyRegions(windowEvents, silenced, from, to);
    if (!deployRegions && a.length === 0) return undefined;
    return [...(deployRegions ?? []), ...a];
  }, [deployRegions, windowEvents, silenced, from, to]);
  const anomalyParam = searchParams.get('anomaly') ?? '';
  const inList = useMemo(() => (anomalyParam ? (anomaliesQ.data?.items ?? []).find(e => e.id === anomalyParam) ?? null : null), [anomalyParam, anomaliesQ.data]);
  // v0.9.465 deseni (streams.tsx rescueQ): ?anomaly= hedefi 24 s/200'lük zarfın
  // dışına düştüyse (paylaşılan link, 60 s sonra kayan liste) tek-olay ucundan çek.
  const rescueQ = useQuery({
    queryKey: ['anomaly-event-rescue', anomalyParam],
    queryFn: () => api.anomalyEvent(anomalyParam),
    enabled: !!anomalyParam && anomaliesQ.data !== undefined && !inList,
    staleTime: 60_000,
  });
  const drawerEvent = inList ?? (rescueQ.data ?? null);
  const openAnomaly = (id: string | null) => setSearchParams(prev => {
    const p = new URLSearchParams(prev);
    if (id) p.set('anomaly', id); else p.delete('anomaly');
    return p;
  }, { replace: true });
  const muteAnomaly = (e: AnomalyEvent, durationSec: number) => {
    // /anomalies sayfasının anahtarı (streams.tsx onMute); sunucu kanonik sha1'e
    // çevirir (v0.10.162 anomaly_extra.go silenceFingerprint) — akış + terfi kapısı okur.
    void createSilence.mutateAsync({ fingerprint: silenceKey(e), kind: e.kind, pattern: e.pattern, service: e.service, durationSec, reason: 'operator: değil (servis sayfası)' });
  };

  // Throughput series (OK vs Errors) derived from the MV-backed rate +
  // error_rate series — no extra query, no raw-spans scan (invariant #3).
  // Errors = rate × err%, OK = the remainder; the two add up to the total rate.
  // (A 4xx-vs-5xx split would need an HTTP-status MV dimension.)
  //
  // v0.9.844 — memo artık TEK liste döndürüyor (OK + Errors). Eskiden iki
  // vardı: `chart` (Toplam + Errors çizgileri) eski motorun ChartCard'ına,
  // `stats` alttaki tabloya gidiyordu. ChartCard dalı sökülünce `chart`
  // tarafının ve onu üreten istemci-toplamının tüketicisi kalmadı; kalan
  // panel iki seriyi rol renkleriyle (success/error) çiziyor. (v0.9.845 —
  // o toplayıcı, sumNullableSeries, öksüz kalınca silindi.)
  const throughput = useMemo<ChartLine[]>(() => {
    // v0.9.253 — `lat` is the entry-scoped series when the service has entry
    // spans, and the all-span series when it doesn't (usingAllSpans). Reading
    // through it keeps throughput, error rate and latency on ONE population:
    // a chart showing entry-span latency above all-span throughput would be
    // two different services stacked on one card.
    const ratePts = lat?.rate?.[0]?.points ?? [];
    const erPts = lat?.error_rate?.[0]?.points ?? [];
    if (ratePts.length < 2) {
      // Tek nokta / veri yok — ayrıştıracak bir şey yok, ham hız çizilir.
      return [{ series: lat?.rate ?? [], color: 'var(--accent)', label: 'Toplam' }];
    }
    const okPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * (1 - (erPts[i]?.value ?? 0) / 100)) }));
    const errPts = ratePts.map((p, i) => ({ time: p.time, value: Math.max(0, p.value * ((erPts[i]?.value ?? 0) / 100)) }));
    const okLine: ChartLine = { series: [{ groupKey: [], points: okPts }], color: 'var(--ok)', label: 'OK' };
    const errLine: ChartLine = { series: [{ groupKey: [], points: errPts }], color: 'var(--err)', label: 'Errors' };
    return [okLine, errLine];
  }, [lat]);

  // v0.9.844 — `metricTputLine` (metrik türevli tek ChartLine) SİLİNDİ: eski
  // motorun alt kartını besliyordu. Tanılama tarafı YAŞIYOR — aşağıdaki
  // MetricThroughputNote artık serinin BOŞ olup olmadığına doğrudan bakıyor
  // (aynı koşul: `!d.series?.length`), çizim tipine değil.
  const metricTputEmpty = (metricTputQ.data?.series?.length ?? 0) === 0;

  // v0.9.798 — metrik throughput'un toplamı: route serilerinin İSTEMCİ
  // tarafında toplamı. EK SORGU YOK ve gerekmiyor — rate TOPLANABİLİR
  // (istek/sn), route'ların toplamı servis genelidir.
  //
  // v0.9.799 (operatör netleştirmesi) — bu toplam artık YALNIZ KPI
  // KAROSUNU besliyor. v0.9.798 onu büyük panele de bir "Toplam" çizgisi
  // olarak koymuştu; operatörün kastı üstteki karolardı ("kalın çizgili
  // hali kötü, eski hali daha iyiydi"), grafik v0.9.796'daki saf route
  // kırılımına döndü. Karo–panel kaynak birliği KORUNUYOR: karo bu
  // toplamı, panel aynı sorgunun route serilerini çiziyor.
  const metricTputTotal = useMemo<SpanMetricSeries | null>(() => {
    const ser = metricTputQ.data?.series ?? [];
    return ser.length > 0 ? sumSeries(ser) : null;
  }, [metricTputQ.data]);

  // ── Response time · metrik (v0.9.774, operatör-bildirimi: prod'da bu
  // panel boş) ─────────────────────────────────────────────────────────
  //
  // KÖK NEDEN: panelin KENDİNE ÖZEL bir veri yolu vardı —
  // /metric-throughput → attachMetricLatency → histogram KOVALARINDAN
  // P50/P95/P99. Prod'daki metric_points satırları bucket_counts taşıyıp
  // bucket_bounds taşımıyor, sınırsız kovadan yüzdelik hesaplanamıyor,
  // yol sessizce boş dönüyordu (sunucu teşhisi: "histYol times=0
  // bounds=0"). Aynı sayfadaki "Response time · P99" KAROSU farklı
  // zincirden (dar rollup tDigest) beslendiği için çalışıyordu — panel
  // boşken karo dolu, en kötü kombinasyon.
  //
  // ÇÖZÜM: panele özel yolu silip Explore'un ÇALIŞAN yoluna bağlamak.
  // Explore aynı metriği ham metric_points üzerinden avgOrNull(value)
  // ile okuyor ve prod'da çalışıyor. Panel artık AYNI ucu, AYNI
  // parametre şekliyle çağırıyor: iki yüzey ayrışamaz, çünkü tek yol var.
  //
  // Metrik ADI hardcode DEĞİL: throughput ucunun keşfettiği ad kullanılır
  // (prod'da http.server.request.duration, lokalde http.server.duration).
  //
  // v0.9.1274 (operatör-bildirimi) — ama `metric` DEĞİL, `rtMetric`. Throughput
  // ucu THROUGHPUT için seri çözüyor ve VictoriaMetrics kurulumunda bu meşru
  // olarak `…_seconds_count` oluyor (histogramın hızı `rate(_count)`). Aşağıdaki
  // İKİ panel ise DEĞER okuyor (agg='avg'): aynı adı taşımak kümülatif bir
  // sayacın ham ortalamasını çizdi ve `_seconds` yüzünden eksen "14.2 weeks"
  // dedi. Uç artık iki adı ayrı veriyor; burası aile adını okur.
  //
  // `||`, `??` DEĞİL: alan eski cache gövdelerinde YOK ve boş string de eski
  // alana düşmeli — `??` boş stringi geçerli sayıp paneli adsız bırakırdı.
  const metricName = metricTputQ.data?.rtMetric || metricTputQ.data?.metric || '';
  // Adım: Explore paritesi (stepForWidth). Genişlik kaynağı kardeş RED
  // panelleriyle AYNI (redMdp = quantizeWidth(içerik/3)/2 nokta), yani
  // px ≈ redMdp×2 — deterministik, rung'a snap'li, cache-key kardinalitesi
  // sınırlı (v0.8.270). mdp GÖNDERİLMİYOR: metricQueryFull'un sabit 1500
  // varsayılanı Explore ile bayt-aynı istek üretir.
  const rtStep = useMemo(
    () => stepForWidth(Math.max(1, (to - from) / 1e9), redMdp * 2),
    [from, to, redMdp]);
  // env(a), v0.9.1041 — the metric Response time panels read metric_points
  // via /api/metrics/query, whose filter compiler resolves
  // deployment.environment through metricEnvExpr (coalescing the ≥1.27 and
  // legacy spellings from res_keys). Adding the conjunct here narrows the
  // metric avg to one env so the Response time tile+chart agree with the
  // env-scoped span RED — no schema change.
  const rtFilters = useMemo(
    () => encodeFilters([
      { k: 'service.name', op: '=', v: [service] },
      ...(env ? [{ k: 'deployment.environment', op: '=' as const, v: [env] }] : []),
    ]),
    [service, env]);
  const rtAvgQ = useQuery({
    queryKey: ['service-rt-avg-route', service, metricName, from, to, rtStep, env],
    enabled: !!metricName,
    staleTime: 30_000,
    queryFn: ({ signal }) => api.metricQueryFull({
      name: metricName,
      agg: 'avg',
      groupBy: 'http.route',
      filters: rtFilters,
      from, to,
      step: rtStep,
    }, signal),
  });
  // Sunucu top-N YOK bu yolda (grup başına seri, 50k satır tavanına dek):
  // kırpma istemcide, ve SÖYLENİYOR.
  const rtView = useMemo(
    () => topRoutesByArea(rtAvgQ.data?.series, ROUTE_TOP_N, rtAvgQ.data?.rowsCapped),
    [rtAvgQ.data]);

  // ── Grupsuz (servis geneli) ortalama — KPI KAROSU için (v0.9.798) ────
  //
  // AYRI SORGU, ve bu pazarlık konusu DEĞİL: ortalama TOPLANMAZ ve
  // ortalanamaz. Route ortalamalarının ortalaması, 3 istek gören bir
  // route ile 3000 istek gören bir route'u eşit ağırlıklar — v0.9.776'nın
  // sunucuda düzelttiği hatanın (ortalamaların ortalaması) istemci
  // tarafındaki birebir ikizi olurdu. Grupsuz sorgu ise sunucuda
  // gözlem-ağırlıklı hesaplanır (sum(sum_value)/sum(count)), yani DOĞRU
  // servis geneli.
  //
  // v0.9.799 — bu seri GRAFİĞE İTEM OLARAK GİRMİYOR (operatör: büyük
  // panelde kalın "Toplam" çizgisi istenmiyordu, istenen üstteki
  // karoydu). Sorgu yaşıyor çünkü Response time KAROSUNUN değeri,
  // sparkline'ı ve vs-prior deltası ondan besleniyor — karo ile panelin
  // aynı METRİKTEN okuması v0.9.774'ün teşhisinin gereği.
  //
  // v0.9.797 sayesinde bu sayı TEMİZ: dışlama kuralları sunucuda
  // grupsuz sorgulara da uygulanıyor, yani healthcheck route'ları
  // ortalamayı aşağı çekmiyor.
  const rtTotalQ = useQuery({
    queryKey: ['service-rt-avg-total', service, metricName, from, to, rtStep, env],
    enabled: !!metricName,
    staleTime: 30_000,
    queryFn: ({ signal }) => api.metricQueryFull({
      name: metricName,
      agg: 'avg',
      // groupBy YOK — servis geneli, gözlem-ağırlıklı.
      filters: rtFilters,
      from, to,
      step: rtStep,
    }, signal),
  });
  // Grupsuz sorgu tek seri döndürür; boşsa karo span türevli P99'a düşer.
  const rtTotalSeries = rtTotalQ.data?.series?.[0];

  const rtUnit = metricUnitToGrafana(metricTputQ.data?.metricUnit);
  const rtNote = [
    // v0.9.799 — not yine SADE: "10 seri · +N daha". v0.9.798'in
    // "Toplam + " öneki, panelde kırpmanın dışında duran bir çizgi
    // olduğu için vardı; o çizgi kalkınca önek yalan olurdu.
    rtView.note,
    // v1 dürüstlük deseni: tanınmayan birimde eksen ham sayı çizer ve
    // bunu SÖYLER — sessizce "ms" yazmak yanlış sayıya güven üretir.
    rtUnit ? null : 'birim tanınmadı',
  ].filter(Boolean).join(' · ') || null;

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

  // ── KPI karoları METRİKTEN (v0.9.798, operatör isteği) ───────────────
  //
  // Response time ve Throughput karoları artık altlarındaki METRİK
  // panelleriyle AYNI kaynaktan besleniyor. Sebep v0.9.774'ün kendi
  // teşhisinde yazılı: panel metrikten, karo span'den okuyunca ikisi
  // ayrışıyor ve operatör hangisine bakacağını bilemiyor ("panel boşken
  // karo dolu, en kötü kombinasyon").
  //
  // Response time karosunun agg'ı AVG, P99 DEĞİL — ve bu bir tercih
  // değil bir SINIR: metrikten P99 prod'da verilemez (metric_points
  // satırları bucket_counts taşıyıp bucket_bounds taşımıyor, sınırsız
  // kovadan yüzdelik hesaplanamıyor — v0.9.774'ün kökü). Karo bunu
  // etiketinde SÖYLÜYOR ("Response time · avg") ve kuyruk görünürlüğü
  // ikincil satırda span P99 olarak duruyor.
  //
  // METRİK BAĞI YOKSA karolar eski span davranışına DÜŞER (boş karo
  // YASAK) ve başlıklarına "· span" eki gelir — dürüstlük: sayı hangi
  // kaynaktan geliyorsa o yazar.
  const metricUnitRaw = metricTputQ.data?.metricUnit;
  // Response time · metrik: grupsuz (gözlem-ağırlıklı) avg serisi, ms'e
  // çevrilmiş. Birim tanınmazsa null → span'e düşülür (TAHMİN YOK).
  const rtAvgMsSeries = useMemo(() => {
    const pts = rtTotalSeries?.points ?? [];
    return pts
      .map(p => metricAvgToMs(p.value, metricUnitRaw))
      .filter((v): v is number => v != null);
  }, [rtTotalSeries, metricUnitRaw]);
  const rtAvgMsNow = rtAvgMsSeries.slice(-1)[0];
  // Throughput · metrik: route toplamının son değeri (kırılım kapalıysa
  // sunucu zaten tek seri döndürür ve toplamı kendisidir).
  const metricRpsSeries = useMemo(
    () => (metricTputTotal?.points ?? []).map(p => p.value),
    [metricTputTotal]);
  const metricRpsNow = metricRpsSeries.slice(-1)[0];

  // Kaynak KARARI karo başına ayrı: metrik throughput'u eşleşmiş ama
  // response-time metriği birim taşımıyor olabilir. Tek bir "metrik modu"
  // bayrağı ikisini birbirine kilitlerdi ve biri yüzünden diğeri de
  // span'e düşerdi.
  const rtFromMetric = rtAvgMsNow != null;
  const tputFromMetric = metricRpsNow != null;
  const rtLabel = rtFromMetric ? 'Response time · avg' : 'Response time · P99 (span)';
  const tputLabel = tputFromMetric ? 'Throughput' : 'Throughput (span)';
  // Karo tooltip'i kaynağı TAM yazar — kapsam rozeti span kapsamını
  // anlatıyor ve metrik karosunda o rozet geçerli DEĞİL.
  const rtTileNote = rtFromMetric
    ? `Kaynak: ${metricName} · gözlem-ağırlıklı ortalama (grupsuz), service.name = ${service}`
    : latScopeNote;
  const tputTileNote = tputFromMetric
    ? `Kaynak: ${metricTputQ.data?.metric ?? '?'} · rate toplamı (route'ların toplamı), eşleşme ${metricTputQ.data?.matchedBy ?? '?'}`
    : latScopeNote;

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
  // v0.9.774 — Response time GRAFİĞİNİN kapı tanımlayıcısı, KAROSUNUNKİ
  // değil. İkisi ayrı zincirden besleniyor (karo: dar rollup tDigest P99;
  // grafik: metrik avg, route kırılımlı) ve tek bir descriptor ikisini
  // birden temsil edemez. ⋮ menüsü panelin GERÇEK sorgusunu yansıtsın:
  // p99 yazıp avg çizmek, doorway'in vaadini ("aynı nesne hem çizer hem
  // açar") boşa çıkarırdı.
  const mkMetricResponseTime = () =>
    metricQuery({
      metric: metricName || 'duration_milliseconds_bucket',
      agg: 'avg', unit: 'ms', filters: svcFilter,
      groupBy: ['http.route'], viz: 'line', range,
    });
  // v0.9.798 — KARONUN kapı tanımlayıcısı: grupsuz (servis geneli) avg,
  // stat vitrini. Grafiğinkinden AYRI olmak zorunda (yukarıdaki v0.9.774
  // gerekçesinin aynısı, bir seviye aşağıda): karo tek bir sayı
  // gösteriyor ve o sayı route kırılımından değil grupsuz sorgudan
  // geliyor. groupBy ile açılan bir Explore, karonun sayısını üretemez.
  const mkMetricResponseTimeTotal = () =>
    metricQuery({
      metric: metricName || 'duration_milliseconds_bucket',
      agg: 'avg', unit: 'ms', filters: svcFilter, viz: 'stat', range,
    });
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
        {/* v0.9.798 (operatör isteği) — SIRA DEĞİŞTİ: Response time önce,
            Throughput sonra, Failure rate yerinde. Altındaki grafik
            şeridinin sırası da bu (Response time · Throughput · Failure
            rate), yani her karo kendi eğrisinin tam üstünde okunuyor —
            v0.9.631'de Failure rate için kurulan hizanın diğer ikiye
            uygulanmış hâli. */}
        <MetricPanel compact title={rtLabel} metricQuery={rtFromMetric ? mkMetricResponseTimeTotal() : mkLatency('p99', 'stat')}>
          <KpiTile lab={rtLabel}
            val={rtFromMetric ? (rtAvgMsNow as number).toFixed(0) : p99Ms.toFixed(0)}
            unit=" ms" accent="var(--orange)"
            spark={rtFromMetric ? rtAvgMsSeries : vals(lat?.p99)}
            delta={computeDelta(rtFromMetric ? rtAvgMsSeries : vals(lat?.p99))}
            goodWhenUp={false} note={rtTileNote}
            // v0.9.798 — İKİNCİL SATIR (tek commit'le geri alınabilir):
            // büyük sayı metrik AVG olunca kuyruk görünürlüğü kaybolmasın.
            // Değer ZATEN çekiliyor (latencyQ) — ek sorgu yok.
            sub={rtFromMetric && p99Now != null ? `P99 (span) · ${p99Ms.toFixed(0)} ms` : undefined}
          />
        </MetricPanel>
        <MetricPanel compact menuOnly title={tputLabel} metricQuery={mkThroughput('stat')}>
          <KpiTile lab={tputLabel}
            val={tputFromMetric
              ? (metricRpsNow as number).toFixed((metricRpsNow as number) < 10 ? 1 : 0)
              : rps.toFixed(rps < 10 ? 1 : 0)}
            unit=" req/s" accent="var(--accent)"
            spark={tputFromMetric ? metricRpsSeries : vals(lat?.rate)}
            delta={computeDelta(tputFromMetric ? metricRpsSeries : vals(lat?.rate))}
            goodWhenUp note={tputTileNote} />
        </MetricPanel>
        {/* v0.9.631 (operatör: "failure rate yüzdesi grafiğin üzerinde olsun,
            p99 ile yer değişsin") — Failure rate karosu SON sırada; altındaki
            RED grafik şeridinin ÜÇÜNCÜ grafiğiyle (Failure rate) aynı kolona
            düşüyor. v0.9.798'de kaynağı DEĞİŞMEDİ: hata oranı span türevli
            (giriş-span ilkesi) ve metrik tarafında karşılığı yok. */}
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
        {/* v0.9.1163 (operatör-raporlu: "çift ⋯") — suppressMenu + fonksiyon
            çocuk: kapı eylemleri (⤢/✎/⟨⟩/⧉) panelin KENDİ ⋯ menüsüne
            menuExtra ile iniyor. Tipi ayrık birleşim tutuyor: bastırıp
            devretmeyi unutan bir çağrı derlenmez (MetricPanel gerekçesi). */}
        <MetricPanel compact menuOnly suppressMenu title="Response time" metricQuery={mkMetricResponseTime()}>
          {(doorway) => (<>
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
          {(
            /* v0.9.736 (operatör düzeni): yalnız METRİK response time —
               span türevli rt-agg/rt-ops panelleri kalktı ("sadece metric
               olan chart kalsın"). Alt karttaki metrik paneli BURAYA
               taşınmıştı; v0.9.844'te eski motor sökülünce bu tek yol
               kaldı. */
            <Suspense key="rt-metric-v2-main" fallback={<Spinner />}>
              <CorePanelMultiLazy
                // v0.9.774 — başlıkta AGG YAZILI. Panel P50/P95/P99
                // çizdiğini iddia ediyordu ve boş geliyordu; artık ne
                // çizdiğini söylüyor: route kırılımlı ORTALAMA.
                // v0.9.1274 (operatör): METRİK ADI BAŞLIKTA YAZMAZ. Ad zaten
                // ⋯ → "Sorguyu göster" metninde ve boş-durum ipucunda duruyor,
                // yani keşfedilebilirlik kaybolmuyor — başlık ise panelin NE
                // çizdiğini söylemeli, hangi seriden okuduğunu değil.
                title="Response time · avg (by route)"
                // storageKey AYNEN: kullanıcının lejant/görünürlük tercihi
                // panel yeniden bağlandı diye sıfırlanmasın.
                // v0.9.794 (operatör bulgusu) — tık hedefi panelin ÇİZDİĞİ
                // kırılımı taşır. v0.9.774'te panel avg BY http.route'a
                // geçti ama tık `by`siz kaldı: Metrics sayfası tek toplam
                // çizgi açıyor, operatör "eskiden route grafiğini
                // gösteriyordu" diyordu. Throughput paneli (:723) doğru
                // deseni zaten taşıyordu.
                menuExtra={doorway}
                storageKey="ov-response-time-metric-v2" height={200} onExpandClick={() => navigate(metricsHref({ by: 'http.route' }))}
                loading={metricTputQ.isLoading || rtAvgQ.isLoading}
                // Birim SUNUCUDAN gelen OTLP birimine göre; tanınmazsa
                // undefined (ham sayı) ve not bunu söyler.
                unit={rtUnit}
                xRange={xRange}
                // v0.9.799 — SAF route kırılımı (v0.9.796 görünümü).
                // v0.9.798'in "Toplam" çizgisi kalktı: operatörün istediği
                // toplam ÜSTTEKİ karodaydı, panelde kalın bir çizgi
                // kırılımı okunmaz hâle getiriyordu.
                items={rtView.items}
                // defaultHidden YOK: tek agg var, gizlenecek P50/P95 de.
                note={rtNote}
                // Hata ve boşluk AYRI: birincisi sorgu patladı, ikincisi
                // sorgu çalıştı ama bu filtrede veri yok. Aynı ekranı
                // göstermek yanlış eyleme yönlendirir.
                error={rtAvgQ.isError
                  ? (rtAvgQ.error instanceof Error ? rtAvgQ.error.message : 'Metrik sorgusu başarısız')
                  : undefined}
                emptyReason={!rtAvgQ.isError && !rtAvgQ.isLoading && rtView.items.length === 0
                  ? (metricName ? 'Bu filtrede veri yok' : 'Servise bağlı metrik bulunamadı')
                  : undefined}
                emptyHint={metricName ? `${metricName} · service.name = ${service}` : undefined}
                // Geliştirici detayı — mevcut ⋯ → "Sorguyu göster". Grupsuz
                // sorgu burada YAZILMAZ (v0.9.799): panel onu çizmiyor,
                // yalnız üstteki karo okuyor.
                queryText={`avg(${metricName || '?'}) by (http.route), service.name="${service}", step=${rtStep}s`
                  + (rtAvgQ.isError ? `\n\nHATA: ${String(rtAvgQ.error)}` : '')}
                regions={chartRegions}
                onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync}
              />
            </Suspense>
          )}
          {/* v0.9.844 — SPAN TÜREVLİ İKİ PANEL BİLİNÇLİ OLARAK GİTTİ:
              "Toplam" (avg/P50/P95/P99) ve "Operasyonlar" (giriş
              operasyonu başına P95) görünümleri + onları seçen ?rtops
              parametresi. Karar v0.9.736'nın operatör düzeniydi ("sadece
              metric olan chart kalsın"); o gün eski motor kaçış kapısı
              olarak yaşadığı için kod duruyordu, bugün o kapı da yok.
              Kuyruk görünürlüğü kaybolmadı: span P99 üstteki karonun
              ikincil satırında (v0.9.798). */}
          </>)}
        </MetricPanel>
        <MetricPanel compact menuOnly suppressMenu title="Throughput" metricQuery={mkThroughput('line')}>
          {(doorway) => (<>
          {/* v0.9.253 — status ve seri artık ENTRY sorgusundan. Kart üstündeki
              KPI giriş span'lerini sayarken altındaki grafiğin tüm span'leri
              çizmesi, aynı kartta iki farklı servisi üst üste koymak olurdu. */}
          {(
            /* v0.9.736 (operatör düzeni): orta slot artık METRİK throughput
               (route kırılımı) — alt karttan buraya taşındı. Span OK/Errors
               paneli 3. slota ("Throughput / Failure rate") geçti. */
            <Suspense key="tput-metric-v2-main" fallback={<Spinner />}>
              <CorePanelMultiLazy
                // v0.9.1274 — ad başlıktan çıktı (kardeş RT paneliyle aynı
                // karar). Çözülen seri ⋯ → "Sorguyu göster" metninde: orada
                // instrument ve eşleşme etiketiyle BİRLİKTE duruyor, yani
                // tanılama için daha iyi bir yer.
                title="Throughput · metrik"
                menuExtra={doorway}
                storageKey="ov-throughput-metric-v2"
                loading={metricTputQ.isLoading}
                height={200} xRange={xRange} onExpandClick={() => navigate(metricsHref({ by: 'http.route' }))}
                unit="reqps"
                // v0.9.799 — SAF route kırılımı (v0.9.796 görünümü).
                // v0.9.798'in istemci-toplamı "Toplam" çizgisi kalktı:
                // toplam ÜSTTEKİ karoda yaşıyor (aynı sumSeries çıktısı),
                // grafikte kalın bir çizgi olarak kırılımı bastırıyordu.
                items={(metricTputQ.data?.series ?? []).map((s0) => ({
                  series: [s0],
                  name: s0.groupKey?.length ? s0.groupKey.join(' · ')
                    : `metrik (${metricTputQ.data?.matchedBy ?? 'job'})`,
                  role: 'data' as const,
                }))}
                regions={chartRegions}
                onZoom={onZoom} onZoomReset={onZoomReset}
                syncKey={chartSync}
                queryText={`metric=${metricTputQ.data?.metric ?? '?'} · instrument=${metricTputQ.data?.instrument ?? '?'} · eşleşme=${metricTputQ.data?.matchedBy ?? '?'} · mdp=${redMdp}`}
              />
            </Suspense>
          )}
          {/* v0.9.665 — TANILAMA. Boş bir grafik "metrik yok" ile "desen
              tutmadı"yı aynı gösterir; ikisi bambaşka eylem gerektiriyor
              (collector'ı düzelt / deseni düzelt). Bu yüzden neden
              yazılıyor, gerçek `job` değerleriyle birlikte. */}
          {/* v0.9.844 — v0.9.675'in "Throughput · metrik" ALT KARTI gitti.
              İçeriği v0.9.736'da zaten ÜSTTEKİ slota taşınmıştı; alt kart
              yalnız eski motorun kaçış kapısında çiziliyordu, yani
              varsayılan görünümde bir yılı aşkın süredir görünmüyordu. */}
          {/* v0.9.683 — HATA DURUMU. Burada yalnız `data` varsa çizim
              vardı: uç 500 dönünce data undefined kalıyor ve HİÇBİR ŞEY
              çizilmiyordu — operatörün gördüğü "panel gelmiyor" tam
              buydu, üstelik sebepsiz. Sessiz başarısızlık, bugün altı
              kez düzelttiğim sınıfın kendi hata yolumdaki hâli. */}
          {metricTputQ.isError && (
            <div style={{
              marginTop: 8, padding: '8px 10px', borderRadius: 6,
              background: 'var(--bg2)', border: '1px solid var(--border)',
              fontSize: 11.5, lineHeight: 1.5, color: 'var(--text2)',
            }}>
              <b>Metrik throughput okunamadı.</b>
              <div style={{ color: 'var(--text3)', marginTop: 4 }}>
                {metricTputQ.error instanceof Error
                  ? metricTputQ.error.message : 'Bilinmeyen hata'}
              </div>
            </div>
          )}
          {metricTputQ.data && metricTputEmpty && (
            <MetricThroughputNote d={metricTputQ.data} />
          )}
          </>)}
        </MetricPanel>
        <MetricPanel compact menuOnly suppressMenu title="Failure rate" metricQuery={mkFailureRate('line')}>
          {(doorway) => (
            /* v0.9.739 (operatör netleştirmesi): GRAFİK 736'daki birleşik
               OK+Errors (req/s) — v0.9.738 grafiği değiştirerek yanlış
               okumuştu; istenen yalnız BAŞLIKTI. "Failure rate · trace"
               adı + span türevli OK/Errors gövdesi.
               v0.9.844 — BİLİNÇLİ KAYIP: % eksenli eski panel (errors %
               + SLO hata-bütçesi eşiği) eski motorla birlikte gitti. Eşik
               çizgisi % eksenine aitti ve bu panel req/s çiziyor; % + SLO
               eşiğini geri isteyen operatör için ayrı bir dilim. */
            <Suspense key="failure-rate-v2" fallback={<Spinner />}>
              <CorePanelMultiLazy
                title={scopedChartTitle('Failure rate · trace', usingAllSpans)}
                menuExtra={doorway}
                storageKey="ov-throughput-failure-v2" height={200} unit="reqps" xRange={xRange}
                loading={latStatus === 'loading'}
                items={[
                  { name: 'OK', role: 'success', series: throughput[0]?.series ?? [] },
                  { name: 'Errors', role: 'error', series: throughput[1]?.series ?? [] },
                ]}
                regions={chartRegions}
                onZoom={onZoom} onZoomReset={onZoomReset} syncKey={chartSync}
              />
            </Suspense>
          )}
        </MetricPanel>
        {/* v0.9.491 — v0.9.476 Apdex grafiği kaldırıldı (operatör kararı);
            RED üçlüsü ov-charts-3'ü tam dolduruyor. */}
      </div>

      {/* v0.10.162 — penceredeki anomaliler TABLOSU (etüt: C'nin tablosu A'nın
          bantlarına aşılandı). Şerit DEĞİL (v0.9.1035 operatör kararı: ikinci
          zaman ekseni yok) — bantlar grafik içinde kalır, tablo yalnız
          anomali varken çizilir ve çakışan bantları satır satır ayırır. */}
      {windowEvents.length > 0 && (
        <div className="anom-win">
          <PanelTitle sub={`${windowEvents.length} anomali · bantlar grafiklerde · satır → çekmece`}>Anomaliler · bu pencere</PanelTitle>
          <AnomalyWindowTable events={windowEvents} silences={silencesQ.data} canEdit={canEditAnomaly}
            onOpen={openAnomaly} onMute={muteAnomaly} truncated={!!anomaliesQ.data?.truncated} />
        </div>
      )}
      {drawerEvent && <Suspense fallback={null}><AnomalyDetailDrawerLazy event={drawerEvent} onClose={() => openAnomaly(null)} /></Suspense>}

      {/* v0.9.1035 (operatör kararı 2026-08-15: "şerit pilot kalksın") —
          v0.9.397'de buraya yayılan annotation şeridi Overview'dan
          KALDIRILDI. Pilot canlıda görüldü ve cevap hayır: aynı olaylar
          zaten chart-İÇİ deploy ▼ çizgileriyle, kendi x-ekseninde ve
          zoom'la birlikte hareket ederek görünüyor; şerit ikinci bir
          zaman ekseni + ikinci bir okuma yükü getiriyordu.
          Chart-içi işaretler HER YERDE kalır — tek desen artık bu.
          Şerit bileşeni Details sekmesinde YAŞIYOR (pages/Service.tsx),
          o yüzden dosya/uç silinmedi; oranın kaderi ayrı bir operatör
          kararı. */}

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
      <ServiceNeighbors service={service} since={nb.since} capped={nb.capped} range={range} defaultOpen />

      {/* AI Analizi — auto-sends this service + selected window (v0.8.89). */}
      <AIAnalysisPanel service={service} rangeS={Math.round((to - from) / 1e9)} />

    </div>
  );
}
