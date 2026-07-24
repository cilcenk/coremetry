import { useMemo } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useServiceDeploys } from '@/lib/queries';
import { Spinner } from '@/components/Spinner';
import { TimeSeriesPanel, type TSSeries } from '@/components/viz/TimeSeriesPanel';
import type { FilterExpr, SpanMetricSeries } from '@/lib/types';
import { podLineLabel } from './runtimePodLabel';
import { smoothPoints } from './runtimeSmooth';

// RuntimeCharts (v0.9.87, operatör talebi) — Service Overview'da dil
// ailesine göre JVM / .NET / Go heap+GC runtime grafikleri.
//
// Teknoloji tespiti: parent Service.tsx'in zaten çektiği
// ['svc-runtime', service] RQ cache'i (DetailsPropsStrip deseni — aynı
// anahtar, staleTime aynı ⇒ SIFIR ek ağ isteği). language =
// telemetry.sdk.language verbatim (java/kotlin → JVM, dotnet/csharp →
// .NET, go → Go).
//
// Metrik adları:
//   JVM  — stable jvm.* semconv (OTel javaagent 2.x; operatör ortamı
//          2.10 + Java 17/21). process.runtime.jvm.* (agent 1.x) BİLE
//          BİLE yok sayılır — operatör teyidi: kullanılmıyor.
//   .NET — process.runtime.dotnet.* (OpenTelemetry.Instrumentation.
//          Runtime, .NET 6/8; stable dotnet.* ancak .NET 9'da geldi).
//   Go   — process.runtime.go.* (otel-go runtime; coremetry'nin kendisi
//          bunu emit eder → lokalde dogfood ile doğrulanabilir).
//
// Sorgu semantiği (keşif bulgusu): histogram instrument'ta value kolonu
// per-export ORTALAMA (Sum/Count, convert.go) ⇒ jvm.gc.duration'a
// agg=avg "ortalama GC pause" verir — DOĞRU; agg=sum "ortalamaların
// toplamı" olurdu (yanlış). Monotonic counter'lar (collections.count,
// exceptions.count…) kümülatif tırmanan çizgi çizerdi — backend'de
// rate/delta yok, v1'de bilerek dışarıda.
//
// Hizalama: her çizgi AYRI /api/metrics/query fetch'i; bucket kümeleri
// farklılaşabilir (zero-fill yok). ChartCard index-eşler ⇒ alignToUnion
// ile union eksene hizalanır, eksik bucket null (grafikte boşluk).

const MB = 1 / (1024 * 1024);
const HEAP: FilterExpr[] = [{ k: 'jvm.memory.type', op: '=', v: ['heap'] }];

interface LineSpec {
  metric: string;
  label: string;      // fanout'ta groupKey ezer
  color: string;      // fanout'ta palet ezer
  filters?: FilterExpr[];
  groupBy?: string;
  scale?: number;     // değer çarpanı (By→MB, s→ms, ns→ms)
  fanout?: boolean;   // groupBy fan-out: dönen her seri ayrı çizgi
  // Fanout etiketi üreticisi (pod kırılımı); yoksa groupKey.join('/').
  labelOf?: (gk: string[], service: string, fallback: string) => string;
  // Referans çizgisi (ör. heap limit) — kesikli çizilir, pod çizgileri
  // ona yaklaşınca "doldu" gözle okunur.
  ref?: boolean;
}

// v0.9.89 (operatör talebi) — JVM/.NET kartları POD BAZLI: "herhangi
// bir pod heap dolduğunda ya da GC yaptığında ayırt edebileyim".
// ÜÇLÜ groupBy (v0.9.91 — operatör "4abcd5 UUID görünüyor, pod ismi
// yazmıyor" raporu): k8s.pod.name (collector k8sattributes'a bağlı, her
// ortamda yok) + host.name (container hostname = k8s pod adı, javaagent
// default) + service.instance.id (UUID son çare). Etiket önceliği
// podLineLabel'da okunabilir pod adına yeğler.
const POD_GROUP = 'resource.k8s.pod.name,resource.host.name,resource.service.instance.id';
// mode (v0.9.128, operatör talebi) — memory kartları (heap/committed, MB) =
// area (dolu kapasite okunur); GC pause / thread / object COUNT kartları =
// line (dolgu yanıltıcı, "biriken alan" anlamı yok). Verilmezse 'area'.
interface CardSpec { key: string; title: string; unit: string; lines: LineSpec[]; mode?: 'area' | 'line' }

// v0.9.95 — 30+ pod'lu servisler için üst sınır yükseldi (eski 8, operatör
// "sadece 8 pod gösteriyor"); pod çizgilerinin rengini TimeSeriesPanel'in
// stabil seriesColor paleti verir (etiket→renk sabit), interaktif lejand
// tablosu pod ayırt/seç eder. Aşırı yüksek pod sayısında (>cap) grafik
// erimesin diye sınır + "N of M" notu.
const FANOUT_MAX = 40;

const FAMILY_CARDS: Record<string, CardSpec[]> = {
  jvm: [
    { key: 'heap', title: 'JVM heap (by pod)', unit: ' MB', lines: [
      { metric: 'jvm.memory.used', label: 'used', color: 'var(--accent)', filters: HEAP,
        scale: MB, groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel },
      // limit tüm pod'larda aynı JVM ayarı — tek kesikli referans çizgisi;
      // pod çizgisi limite yaklaşınca "doldu" gözle okunur.
      { metric: 'jvm.memory.limit', label: 'limit', color: 'var(--err)', filters: HEAP, scale: MB, ref: true },
    ] },
    { key: 'gc', title: 'GC pause by pod (avg)', unit: ' ms', mode: 'line', lines: [
      { metric: 'jvm.gc.duration', label: 'gc', color: 'var(--warn)',
        groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel, scale: 1000 },
    ] },
    { key: 'threads', title: 'Threads (by pod)', unit: '', mode: 'line', lines: [
      { metric: 'jvm.thread.count', label: 'threads', color: 'var(--teal)',
        groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel },
    ] },
  ],
  dotnet: [
    { key: 'heap', title: '.NET heap (by pod)', unit: ' MB', lines: [
      { metric: 'process.runtime.dotnet.gc.heap.size', label: 'heap', color: 'var(--accent)',
        groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel, scale: MB },
    ] },
    { key: 'committed', title: 'GC committed (by pod)', unit: ' MB', lines: [
      { metric: 'process.runtime.dotnet.gc.committed_memory.size', label: 'committed', color: 'var(--purple)',
        groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel, scale: MB },
    ] },
    { key: 'threads', title: 'ThreadPool threads (by pod)', unit: '', mode: 'line', lines: [
      { metric: 'process.runtime.dotnet.thread_pool.threads.count', label: 'threads', color: 'var(--teal)',
        groupBy: POD_GROUP, fanout: true, labelOf: podLineLabel },
    ] },
  ],
  go: [
    { key: 'heap', title: 'Go heap', unit: ' MB', lines: [
      { metric: 'process.runtime.go.mem.heap_inuse', label: 'in-use', color: 'var(--accent)', scale: MB },
      { metric: 'process.runtime.go.mem.heap_sys', label: 'sys', color: 'var(--purple)', scale: MB },
      { metric: 'process.runtime.go.mem.heap_alloc', label: 'alloc', color: 'var(--teal)', scale: MB },
    ] },
    { key: 'gc', title: 'GC pause (avg)', unit: ' ms', mode: 'line', lines: [
      { metric: 'process.runtime.go.gc.pause_ns', label: 'pause', color: 'var(--warn)', scale: 1 / 1e6 },
    ] },
    { key: 'objects', title: 'Heap objects', unit: '', mode: 'line', lines: [
      { metric: 'process.runtime.go.mem.heap_objects', label: 'objects', color: 'var(--teal)' },
    ] },
  ],
};

function familyOf(language: string | undefined): string | null {
  const l = (language || '').toLowerCase();
  if (l === 'java' || l === 'kotlin') return 'jvm';
  if (l === 'dotnet' || l === 'csharp') return 'dotnet';
  if (l === 'go') return 'go';
  return null;
}

const FAMILY_TITLE: Record<string, string> = { jvm: 'JVM', dotnet: '.NET', go: 'Go' };

export function RuntimeCharts({ service, from, to, onZoom, onZoomReset }: {
  service: string;
  from: number; // unix ns (parent'ın çözülmüş penceresi — RQ anahtarı hizalı)
  to: number;
  onZoom?: (fromSec: number, toSec: number) => void;
  // Grafana-parite M1 — çift-tık: Service.tsx zoom geri-yığınını pop eder.
  onZoomReset?: () => void;
}) {
  // Parent Service.tsx ile AYNI anahtar → RQ cache'inden dolar, ek istek yok.
  const runtimeQ = useQuery({
    queryKey: ['svc-runtime', service],
    queryFn: () => api.serviceRuntime(service),
    enabled: !!service,
    staleTime: 300_000,
  });
  const family = familyOf(runtimeQ.data?.language);
  // Modül sabiti referansı ya da stabil boş dizi — render başına yeni []
  // kimliği aşağıdaki useMemo'ları boşa tetiklerdi (eslint exhaustive-deps).
  const cards = useMemo(() => (family ? FAMILY_CARDS[family] : []), [family]);

  // Madde 4 sweep — deploy ▼ marker'ları runtime panellerine: "heap spike
  // deploy'dan mı?" bir bakışta okunsun. AYNI (service, from, to) anahtarı
  // ServiceCharts'ın useServiceDeploys'uyla RQ-dedupe olur — ek istek yok.
  // TSP `deploys` bare unix-ns bekler; memo, taze dizi kimliğinin rebuild
  // tetiklemesini önler.
  const deploysQ = useServiceDeploys(service, from, to);
  const deployNs = useMemo(() => {
    const d = deploysQ.data;
    return d && d.length ? d.map(x => x.timeUnixNs) : undefined;
  }, [deploysQ.data]);

  // Kart başına değil ÇİZGİ başına sorgu (metrik+filtre+groupBy farklı).
  const specs = useMemo(
    () => cards.flatMap(c => c.lines.map(l => ({ card: c, line: l }))),
    [cards],
  );
  const queries = useQueries({
    queries: specs.map(({ line }) => ({
      queryKey: ['svc-runtime-metric', service, line.metric, line.groupBy ?? '',
        line.filters ? JSON.stringify(line.filters) : '', from, to],
      queryFn: () => api.metricQuery({
        name: line.metric,
        service,
        agg: 'avg',
        filters: line.filters ? JSON.stringify(line.filters) : undefined,
        groupBy: line.groupBy,
        from, to, step: 0,
      }).then(r => r ?? []),
      staleTime: 60_000,
      enabled: !!family,
    })),
  });

  const built = useMemo(() => {
    if (!family) return null;
    let anyData = false;
    const byCard = cards.map(card => {
      // Her çizgi → bir TSSeries. TSP x-eksenini KENDİ union'lar (ayrı
      // fetch'lerin farklı bucket kümeleri iç hizalanır) — elle alignToUnion
      // GEREKMEZ. points ns cinsinde kalır (TSSeries ns bekler). Fanout pod
      // çizgilerinin rengini vermeyiz → TSP seriesColor(label) stabil palet
      // atar; lejand tablosu pod ayırt/seç eder.
      const tsSeries: TSSeries[] = [];
      let podTotal = 0;
      let capped = false;
      specs.forEach(({ card: c, line }, qi) => {
        if (c.key !== card.key) return;
        const data = (queries[qi]?.data ?? []) as SpanMetricSeries[];
        const scale = line.scale ?? 1;
        // v0.9.127 (operatör: "çok zigzag") — heap/GC gauge'u gerçek GC
        // sawtooth'u; hareketli-ortalama ile yumuşat (smoothPoints, pencere
        // serinin kendi nokta sayısından). Referans çizgisi (limit) sabit →
        // SMA değiştirmez. Ölçek önce, sonra smooth.
        if (line.fanout) {
          podTotal += data.length;
          if (data.length > FANOUT_MAX) capped = true;
          data.slice(0, FANOUT_MAX).forEach(s => {
            const label = line.labelOf
              ? line.labelOf(s.groupKey, service, line.label)
              : (s.groupKey.filter(Boolean).join('/') || line.label);
            tsSeries.push({
              label,
              points: smoothPoints(s.points.map(p => ({ time: p.time, value: p.value == null ? null : p.value * scale }))),
            });
          });
        } else {
          const s = data[0];
          if (s) tsSeries.push({
            label: line.label,
            color: line.color,
            dash: line.ref ? [5, 4] : undefined, // referans (limit) kesikli
            points: smoothPoints(s.points.map(p => ({ time: p.time, value: p.value == null ? null : p.value * scale }))),
          });
        }
      });
      if (tsSeries.some(s => s.points.length)) anyData = true;
      const cardQs = specs
        .map(({ card: c }, qi) => ({ c, q: queries[qi] }))
        .filter(x => x.c.key === card.key);
      const status: 'loading' | 'error' | 'ready' =
        cardQs.some(x => x.q.isError) ? 'error'
        : cardQs.some(x => x.q.isPending) ? 'loading'
        : 'ready';
      return { card, series: tsSeries, status, podTotal, capped };
    });
    return { byCard, anyData };
  }, [family, cards, specs, queries, service]);

  // Aile bilinmiyor ya da (yüklendi + hiç veri yok) → section hiç yok
  // (ServiceInfraTab:327 emsali — runtime metriği göndermeyen servise
  // boş kart duvarı gösterme).
  const settled = queries.length > 0 && queries.every(q => !q.isPending);
  if (!family || !built || (settled && !built.anyData)) return null;
  if (!settled && !built.anyData) return null; // ilk yükleme: flash yok

  return (
    <>
      <div style={{ marginTop: 18, marginBottom: 8 }}>
        <div style={{ fontSize: 13, fontWeight: 700, color: 'var(--text)' }}>
          Runtime — {FAMILY_TITLE[family]}
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)' }}>
          {family === 'jvm' && <>Auto-instrumentation <code>jvm.*</code> metrics per pod (heap · GC · threads). Lejantta bir pod'a tıkla = izole.</>}
          {family === 'dotnet' && <>Runtime instrumentation <code>process.runtime.dotnet.*</code> metrics per pod.</>}
          {family === 'go' && <>Go runtime <code>process.runtime.go.*</code> metrics.</>}
        </div>
      </div>
      {/* Full-width istifli kartlar — 30+ pod çizgisi + lejand tablosu için
          3-across dar kalıyordu (operatör raporu). Ortak syncKey: bir karta
          hover TÜM kartlarda crosshair çizer (heap spike ↔ GC pause korele). */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {built.byCard.map(({ card, series, status, podTotal, capped }) => (
          <div key={card.key} className="card">
            <div className="ov-card-h">
              <h3>{card.title}{card.unit ? ` (${card.unit.trim()})` : ''}</h3>
              {capped && (
                <span style={{ fontSize: 11, color: 'var(--text3)' }}>
                  {FANOUT_MAX} / {podTotal} pod gösteriliyor
                </span>
              )}
            </div>
            <div className="ov-card-b" style={{ paddingTop: 10, paddingBottom: 10 }}>
              {status === 'loading' && series.length === 0 ? (
                <div style={{ height: 240, display: 'grid', placeItems: 'center' }}><Spinner /></div>
              ) : status === 'error' && series.length === 0 ? (
                <div style={{ height: 240, display: 'grid', placeItems: 'center', color: 'var(--err)', fontSize: 12 }}>
                  Failed to load metrics
                </div>
              ) : series.length === 0 ? (
                <div style={{ height: 240, display: 'grid', placeItems: 'center', color: 'var(--text3)', fontSize: 12 }}>
                  No data in this window
                </div>
              ) : (
                <TimeSeriesPanel
                  series={series}
                  height={240}
                  mode={card.mode ?? 'area'}
                  syncKey={`runtime:${service}`}
                  deploys={deployNs}
                  onZoom={onZoom}
                  onZoomReset={onZoomReset}
                  smooth
                />
              )}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}
