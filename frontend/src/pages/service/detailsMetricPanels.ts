import type { MetricInfo } from '@/lib/types';

// detailsMetricPanels — v0.9.784. Service /details sekmesinin "Metrikler"
// bölümünü KATALOGDAN kuran saf çekirdek.
//
// NEDEN katalog-güdümlü: Details'te bugüne dek metric_points türevli TEK
// panel yoktu (hepsi span türevliydi). Metrik ADI hardcode edilseydi tek
// bir kurulumun adlandırma stiline mıhlanırdık — lokal demo
// `process.runtime.goroutines` basarken coremetry'nin kendisi
// `process.runtime.go.goroutines` basıyor, prod'un HTTP süresi
// `http.server.request.duration`, lokalinki `http.server.duration`. Panel
// seti bu yüzden servisin KENDİ kataloğundan (api.metricNamesSearch)
// türetilir: aile katalogda yoksa panel HİÇ kurulmaz.
//
// KRİTİK KARAR — histogram ailesinde p95 DEĞİL avg (v0.9.774 dersi):
// prod'un metric_points satırları bucket_counts taşıyıp bucket_bounds
// taşımıyor; sınırsız kovadan yüzdelik hesaplanamıyor ve panel SESSİZCE
// boş kalıyor. avg, value kolonunun per-export ortalama semantiğiyle
// (v0.9.776 metricAvgExpr) matematiksel olarak doğru ve prod'da dolu.
// Bu kararı "daha iyi olur" diye p95'e çevirmek 774'ü geri getirir.
//
// SAF: DOM yok, fetch yok, Date yok — tablo-güdümlü vitest'i yanında.

export interface DetailsPanelMetric {
  /** Katalogdan gelen metrik adı — sorguya AYNEN gider. */
  name: string;
  /** Lejant etiketi (panelde birden çok seri varsa ayırt eder). */
  label: string;
}

export interface DetailsPanelSpec {
  /** storageKey / React key eki — kararlı, ada bağlı DEĞİL. */
  key: string;
  title: string;
  /** Yalnız iki değer: sayaçlarda rate, geri kalan her şeyde avg. */
  agg: 'avg' | 'rate';
  /** @grafana/data birim kimliği; undefined = ham sayı (birim yok). */
  unit?: string;
  /** SADECE kural tablosunda yazan ailede dolu (DB havuzu → state). */
  groupBy?: string;
  metrics: DetailsPanelMetric[];
}

// ── Birim eşlemesi ───────────────────────────────────────────────────────
//
// OTLP birimi (UCUM) → @grafana/data birim kimliği. Bilinmeyen ve
// ANNOTATION birimleri ({connection}, {request}) undefined döner: eksen ham
// sayı çizer. Sessizce "ms" varsaymak yanlış sayıya güven üretir
// (v0.9.774 dürüstlük deseni).
//
// '1' (boyutsuz) BİLİNEN bir birimdir ve karşılığı "birim yok"tur — bu
// yüzden haritada AÇIKÇA duruyor, `default` dalına düşmüyor.
const UNIT_MAP: Record<string, string | undefined> = {
  'By': 'bytes',
  'ms': 'ms',
  's': 's',
  'ns': 'ns',
  'us': 'µs',
  'µs': 'µs',
  '%': 'percent',
  '1': undefined,
  '': undefined,
};

export function otlpUnitToGrafana(unit: string | undefined): string | undefined {
  const t = (unit ?? '').trim();
  return Object.prototype.hasOwnProperty.call(UNIT_MAP, t) ? UNIT_MAP[t] : undefined;
}

// ── Ad eşleme yardımcıları ───────────────────────────────────────────────

function afterPrefix(name: string, prefix: string): string | null {
  return name.startsWith(prefix) ? name.slice(prefix.length) : null;
}

/** Listedeki sıra = panel içi seri sırası; yoksa null (eşleşme yok). */
function rankIn(list: readonly string[], v: string | null): number | null {
  if (v == null) return null;
  const i = list.indexOf(v);
  return i < 0 ? null : i;
}

/**
 * `…gc.pause…` — İKİ adlandırma stili (process.runtime.gc.pause ve
 * process.runtime.go.gc.pause_ns) tek kuralla. `pause_total_ns` DIŞARIDA:
 * kümülatif TOPLAM'ın ortalaması "ortalama duraklama" değildir, monoton
 * tırmanan bir çizgi çizer (RuntimeCharts'ın aynı gerekçesi).
 */
function gcPauseRank(name: string): number | null {
  const segs = name.split('.');
  for (let i = 0; i + 1 < segs.length; i++) {
    if (segs[i] !== 'gc') continue;
    const next = segs[i + 1];
    if (next.startsWith('pause') && !next.includes('total')) return 0;
  }
  return null;
}

// ── Kural tablosu ────────────────────────────────────────────────────────

interface Rule {
  key: string;
  title: string;
  agg: 'avg' | 'rate';
  groupBy?: string;
  /** Aile birimi SABİT (oran / yüzde) — katalog birimi okunmaz. */
  unitOverride?: string;
  /** Gerekli instrument(lar); verilmezse hepsi kabul. */
  instrument?: readonly string[];
  rank(name: string): number | null;
  label(name: string): string;
  /** Metrik-başına birim (aile geneli yetmediğinde). */
  unitOf?(name: string, info: MetricInfo): string | undefined;
}

const HTTP_RATE_TAILS = ['requests', 'request.count', 'request_count'] as const;
const HTTP_DUR_TAILS = ['duration', 'request.duration'] as const;
const INFLIGHT_TAILS = ['active_requests', 'queued_requests'] as const;
const DB_POOL_TAILS = ['usage', 'max', 'idle'] as const;
const GOROUTINE_NAMES = [
  'process.runtime.goroutines',      // demo / otel-go 0.x stili
  'process.runtime.go.goroutines',   // coremetry-monolithic'in kendi stili
] as const;

const HIST = ['histogram', 'exp_histogram'] as const;

const RULES: readonly Rule[] = [
  // 1 — HTTP istek hızı. Monoton sayaç → agg='rate' (backend
  // QueryMetricRate: reset-korumalı per-saniye delta, PromQL semantiği).
  // agg='sum' kümülatif tırmanma çizerdi.
  {
    key: 'http-rate',
    title: 'HTTP istek hızı',
    agg: 'rate',
    unitOverride: 'reqps',
    instrument: ['sum'],
    rank: n => rankIn(HTTP_RATE_TAILS, afterPrefix(n, 'http.server.')),
    label: () => 'istek',
  },
  // 2 — HTTP süre ORTALAMASI. p95 YASAK (dosya başlığı: v0.9.774).
  {
    key: 'http-duration',
    title: 'HTTP süre ortalaması',
    agg: 'avg',
    instrument: HIST,
    rank: n => rankIn(HTTP_DUR_TAILS, afterPrefix(n, 'http.server.')),
    label: () => 'ortalama',
  },
  // 3 — Aktif / kuyruk istekler: iki metrik TEK kartta iki seri (ikisi de
  // boyutsuz gauge, aynı eksende okunur). instrument'ta 'sum' da kabul:
  // UpDownCounter OTLP'de non-monotonic Sum olarak iner, gauge olarak
  // değil — yalnız gauge'a bakmak prod'da paneli gizlerdi.
  {
    key: 'http-inflight',
    title: 'Aktif / kuyruk istekler',
    agg: 'avg',
    instrument: ['gauge', 'sum'],
    rank: n => rankIn(INFLIGHT_TAILS, n.split('.').slice(-1)[0]),
    label: n => (n.endsWith('active_requests') ? 'aktif' : 'kuyruk'),
  },
  // 4 — DB bağlantı havuzu. groupBy='state' TEK burada: semconv
  // db.client.connections.usage'ın state attr'ı (used|idle) sabit ve
  // düşük-kardinaliteli. Attr yoksa sunucu tek boş-anahtarlı seri döner
  // (groupKeyExprMetric bilinmeyen anahtarda '' üretir) — zararsız.
  {
    key: 'db-pool',
    title: 'DB bağlantı havuzu',
    agg: 'avg',
    groupBy: 'state',
    rank: n => rankIn(DB_POOL_TAILS, afterPrefix(n, 'db.client.connections.')),
    label: n => n.split('.').slice(-1)[0],
  },
  // 5 — Süreç belleği. `process.` ile başlayıp `memory` SEGMENTİ taşıyan
  // adlar: process.runtime.memory.rss, process.memory.usage. Go'nun
  // `process.runtime.go.mem.heap_*` ailesi BİLEREK dışarıda — `mem`
  // segmenti `memory` değil ve o aile zaten Pods sekmesinde
  // RuntimeCharts'ın pod-kırılımlı kartlarında yaşıyor (ikizleme yok).
  // By → 'bytes': ölçekleme DİSPLAY katmanında, elle çarpan YOK.
  {
    key: 'process-memory',
    title: 'Süreç belleği',
    agg: 'avg',
    rank: n => {
      const segs = n.split('.');
      return segs[0] === 'process' && segs.includes('memory') ? 0 : null;
    },
    label: n => {
      const segs = n.split('.');
      const i = segs.indexOf('memory');
      return segs.slice(i + 1).join('.') || 'memory';
    },
  },
  // 6 — CPU kullanımı. Değer 0-1 aralığında gelir; ×100 ELLE YAPILMAZ —
  // 'percentunit' display id'si 0-1'i yüzde olarak biçimler (dataFrame
  // sözleşmesi: ham değer taşınır, birim gösterimi çözer).
  {
    key: 'cpu-utilization',
    title: 'CPU kullanımı',
    agg: 'avg',
    unitOverride: 'percentunit',
    rank: n => (n === 'cpu.utilization' || n.endsWith('.cpu.utilization') ? 0 : null),
    label: n => n.replace(/\.?cpu\.utilization$/, '') || 'cpu',
  },
  // 7a — Goroutine sayısı. İKİ adlandırma stili de eşlenir.
  {
    key: 'runtime-goroutines',
    title: 'Goroutine sayısı',
    agg: 'avg',
    rank: n => rankIn(GOROUTINE_NAMES, n),
    label: () => 'goroutines',
  },
  // 7b — GC duraklaması. Goroutine'den AYRI panel: biri adet, diğeri
  // süre; tek kartta birleştirmek birim karıştırma olurdu (ev kuralı).
  // Birim ADDAN çözülür (`…_ns` → ns): otel-go bu metriği birimsiz
  // yayıyor, katalog boş 'unit' döndürüyor.
  {
    key: 'runtime-gc',
    title: 'GC duraklaması (ort.)',
    agg: 'avg',
    rank: gcPauseRank,
    label: () => 'gc pause',
    unitOf: (n, info) => (n.endsWith('_ns') ? 'ns' : otlpUnitToGrafana(info.unit)),
  },
];

/**
 * buildDetailsMetricPanels — servisin metrik kataloğu → kurulacak panel
 * listesi. Katalogda ailesi OLMAYAN panel hiç kurulmaz (iki aşamalı
 * gizlemenin birinci aşaması; ikincisi "kurulu ama veri boş" ve o
 * bileşende yaşar).
 *
 * İki değişmez:
 *  - Bir metrik EN FAZLA bir panele girer (kurallar sırayla talep eder).
 *  - Bir panelin birimi TEK: birinci serinin birimi panelin birimidir,
 *    birimi ayrışan aday panele ALINMAZ (birim karıştırma yasağı).
 */
export function buildDetailsMetricPanels(catalog: MetricInfo[]): DetailsPanelSpec[] {
  const claimed = new Set<string>();
  const out: DetailsPanelSpec[] = [];
  // Ada göre tekilleştir: sunucu kataloğu zaten `GROUP BY metric` ile
  // gelir ama iki katalog birleştirilirse (ya da bir gün sayfalama
  // eklenirse) aynı ad iki kez düşer ve panel ÇİFT seri çizerdi.
  const uniq = new Map<string, MetricInfo>();
  for (const info of catalog ?? []) {
    const name = (info?.name ?? '').trim();
    if (name && !uniq.has(name)) uniq.set(name, info);
  }
  for (const rule of RULES) {
    const hits: { name: string; rank: number; unit: string | undefined }[] = [];
    for (const [name, info] of uniq) {
      if (claimed.has(name)) continue;
      if (rule.instrument && !rule.instrument.includes((info.type ?? '').toLowerCase())) continue;
      const r = rule.rank(name);
      if (r == null) continue;
      hits.push({
        name,
        rank: r,
        unit: rule.unitOverride
          ?? (rule.unitOf ? rule.unitOf(name, info) : otlpUnitToGrafana(info.unit)),
      });
    }
    if (hits.length === 0) continue;
    hits.sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name));
    const unit = hits[0].unit;
    const kept = hits.filter(h => h.unit === unit);
    for (const h of kept) claimed.add(h.name);
    out.push({
      key: rule.key,
      title: rule.title,
      agg: rule.agg,
      ...(unit ? { unit } : {}),
      ...(rule.groupBy ? { groupBy: rule.groupBy } : {}),
      metrics: kept.map(h => ({ name: h.name, label: rule.label(h.name) })),
    });
  }
  return out;
}
