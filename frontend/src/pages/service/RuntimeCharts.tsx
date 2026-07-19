import { useMemo } from 'react';
import { useQuery, useQueries } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { alignToUnion } from '@/lib/chart/alignSeries';
import { ChartCard, type ChartLine } from './charts/ChartCard';
import type { FilterExpr, SpanMetricSeries } from '@/lib/types';

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
}
interface CardSpec { key: string; title: string; unit: string; lines: LineSpec[] }

const FANOUT_PALETTE = ['var(--accent)', 'var(--purple)', 'var(--teal)', 'var(--warn)', 'var(--orange)', 'var(--err)'];
const FANOUT_MAX = 6; // groupBy patlamasına karşı üst sınır (legend okunur kalsın)

const FAMILY_CARDS: Record<string, CardSpec[]> = {
  jvm: [
    { key: 'heap', title: 'JVM heap', unit: ' MB', lines: [
      { metric: 'jvm.memory.used', label: 'used', color: 'var(--accent)', filters: HEAP, scale: MB },
      { metric: 'jvm.memory.committed', label: 'committed', color: 'var(--purple)', filters: HEAP, scale: MB },
      { metric: 'jvm.memory.limit', label: 'limit', color: 'var(--err)', filters: HEAP, scale: MB },
    ] },
    { key: 'gc', title: 'GC pause (avg)', unit: ' ms', lines: [
      { metric: 'jvm.gc.duration', label: 'gc', color: 'var(--warn)', groupBy: 'jvm.gc.name', fanout: true, scale: 1000 },
    ] },
    { key: 'threads', title: 'Threads', unit: '', lines: [
      { metric: 'jvm.thread.count', label: 'threads', color: 'var(--teal)' },
    ] },
  ],
  dotnet: [
    { key: 'heap', title: '.NET heap (by gen)', unit: ' MB', lines: [
      { metric: 'process.runtime.dotnet.gc.heap.size', label: 'heap', color: 'var(--accent)', groupBy: 'generation', fanout: true, scale: MB },
    ] },
    { key: 'committed', title: 'GC committed', unit: ' MB', lines: [
      { metric: 'process.runtime.dotnet.gc.committed_memory.size', label: 'committed', color: 'var(--purple)', scale: MB },
    ] },
    { key: 'threads', title: 'ThreadPool threads', unit: '', lines: [
      { metric: 'process.runtime.dotnet.thread_pool.threads.count', label: 'threads', color: 'var(--teal)' },
    ] },
  ],
  go: [
    { key: 'heap', title: 'Go heap', unit: ' MB', lines: [
      { metric: 'process.runtime.go.mem.heap_inuse', label: 'in-use', color: 'var(--accent)', scale: MB },
      { metric: 'process.runtime.go.mem.heap_sys', label: 'sys', color: 'var(--purple)', scale: MB },
      { metric: 'process.runtime.go.mem.heap_alloc', label: 'alloc', color: 'var(--teal)', scale: MB },
    ] },
    { key: 'gc', title: 'GC pause (avg)', unit: ' ms', lines: [
      { metric: 'process.runtime.go.gc.pause_ns', label: 'pause', color: 'var(--warn)', scale: 1 / 1e6 },
    ] },
    { key: 'objects', title: 'Heap objects', unit: '', lines: [
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

export function RuntimeCharts({ service, from, to, onZoom, xRange }: {
  service: string;
  from: number; // unix ns (parent'ın çözülmüş penceresi — RQ anahtarı hizalı)
  to: number;
  onZoom?: (fromSec: number, toSec: number) => void;
  xRange?: { from: number; to: number } | null;
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

  // Kart başına değil ÇİZGİ başına sorgu (metrik+filtre+groupBy farklı).
  const specs = useMemo(
    () => cards.flatMap(c => c.lines.map(l => ({ card: c, line: l }))),
    [cards],
  );
  const queries = useQueries({
    queries: specs.map(({ line }) => ({
      queryKey: ['svc-runtime-metric', service, line.metric, line.groupBy ?? '', from, to],
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
      // Bu karta ait çizgi sonuçlarını topla (fanout genişletmesiyle).
      const raw: { label: string; color: string; points: { time: number; value: number | null }[] }[] = [];
      specs.forEach(({ card: c, line }, qi) => {
        if (c.key !== card.key) return;
        const data = (queries[qi]?.data ?? []) as SpanMetricSeries[];
        const scale = line.scale ?? 1;
        const seriesList = line.fanout ? data.slice(0, FANOUT_MAX) : data.slice(0, 1);
        seriesList.forEach((s, si) => {
          const label = line.fanout
            ? (s.groupKey.filter(Boolean).join('/') || line.label)
            : line.label;
          const color = line.fanout ? FANOUT_PALETTE[si % FANOUT_PALETTE.length] : line.color;
          raw.push({
            label, color,
            points: s.points.map(p => ({ time: p.time, value: p.value == null ? null : p.value * scale })),
          });
        });
      });
      if (raw.some(r => r.points.length)) anyData = true;
      // Ayrı fetch'ler → union eksene hizala; ChartCard index-eşler.
      const aligned = alignToUnion(raw.map(r => r.points));
      const lines: ChartLine[] = raw.map((r, i) => ({
        label: r.label, color: r.color,
        series: [{
          groupKey: [r.label],
          points: aligned.times.map((t, j) => ({ time: t, value: aligned.cols[i][j] })),
        } as SpanMetricSeries],
      }));
      const cardQs = specs
        .map(({ card: c }, qi) => ({ c, q: queries[qi] }))
        .filter(x => x.c.key === card.key);
      const status: 'loading' | 'error' | 'ready' =
        cardQs.some(x => x.q.isError) ? 'error'
        : cardQs.some(x => x.q.isPending) ? 'loading'
        : 'ready';
      return { card, lines, status };
    });
    return { byCard, anyData };
  }, [family, cards, specs, queries]);

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
          {family === 'jvm' && <>Auto-instrumentation <code>jvm.*</code> metrics (heap · GC · threads).</>}
          {family === 'dotnet' && <>Runtime instrumentation <code>process.runtime.dotnet.*</code> metrics.</>}
          {family === 'go' && <>Go runtime <code>process.runtime.go.*</code> metrics.</>}
        </div>
      </div>
      <div className="ov-charts-3">
        {built.byCard.map(({ card, lines, status }) => (
          <ChartCard key={card.key} title={card.title} unit={card.unit}
            lines={lines} status={status} onZoom={onZoom} xRange={xRange} />
        ))}
      </div>
    </>
  );
}
