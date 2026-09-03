// volumeSeries.ts — /traces üst histogramının SAF konfig katmanı (v0.9.843).
//
// Neden ayrı dosya: "hangi seri hangi eksende" bir DÜZEN kararıdır ve
// v0.9.843'te operatör isteğiyle bir kez TAKAS edildi (süre sağdan sola,
// span sayısı soldan sağa — Grafana düzeni). Bir daha sessizce geri
// dönmesin diye eşleme tablo-testli; VolumeChart.tsx TimeChart→uPlot'a
// bağlı olduğundan node ortamındaki vitest onu import edemezdi, bu modül
// ise saf (tip dışında hiçbir çizim bağımlılığı yok).
//
// EKSEN SÖZLEŞMESİ (v0.9.843): SÜRE hep SOL eksende, SAYI hep SAĞ eksende.
// TimeChart sol ekseni fmtLeft ile, sağ ekseni fmtRight ile biçimlendirir —
// yani takasla birlikte biçimlendirici de taşınmak ZORUNDA (sol: ms/s,
// sağ: TimeChart'ın fmtAxisTick kısaltması "30.9M"). VolumeChart bu yüzden
// fmtLeft={fmtVolumeDuration} geçer ve fmtRight vermez.

import type { SpanMetricSeries } from '@/lib/types';
import type { TimeChartSeries } from '@/components/charts/TimeChart';
import { statusColor } from '@/lib/statusColor';

/** Süre ekseni formatı (v0.9.73; v0.9.843'e dek SAĞ eksendeydi, artık SOL):
 *  <1000ms ms, aksi s. "3100 ms" gibi okunmaz büyük değerleri "3.1s" yapar;
 *  küçük gecikmelerde tam sayı ms okunur. Değer+birim şablonu → her birim
 *  dalı testli (v0.6.36 birim-karışımı disiplini). */
export function fmtVolumeDuration(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(v >= 10000 ? 0 : 1)}s`;
  return `${v.toFixed(v < 10 ? 1 : 0)}ms`;
}

export interface VolumeChartConfig {
  /** unix saniye, artan — paylaşılan x ekseni. */
  times: number[];
  series: TimeChartSeries[];
  /** bucket genişliği (dakika), başlık ipucu için. */
  bucketMin: number;
}

/** buildVolumeSeries — count/errors/p50 serilerini TimeChart konfigine çevirir.
 *  Çizim sırası = üst üste binme sırası: tam bar (accent) önce, hata payı
 *  (kırmızı) üstüne — böylece her barın DİBİNDE okunur; p50 çizgisi en son. */
/** v0.10.268 — çubuk etiketi: servis seçiliyken giriş span'i = istek ≈ trace
 *  ("traces"); servissiz pencerede her hop sayılır ("requests"). SAF. */
export function volumeUnitLabel(serviceScoped: boolean): string {
  return serviceScoped ? 'traces' : 'requests';
}

export function buildVolumeSeries(
  count: SpanMetricSeries[] | null,
  errors: SpanMetricSeries[] | null,
  p50: SpanMetricSeries[] | null,
  unit: string = 'traces',
): VolumeChartConfig {
  const cPts = count?.[0]?.points ?? [];
  if (!cPts.length) return { times: [], series: [], bucketMin: 1 };
  const eMap = new Map((errors?.[0]?.points ?? []).map(p => [p.time, p.value]));
  const pMap = new Map((p50?.[0]?.points ?? []).map(p => [p.time, p.value]));
  const t = cPts.map(p => Math.round(p.time / 1e9)); // ns → unix sec
  const total = cPts.map(p => p.value);
  const err = cPts.map(p => Math.min(eMap.get(p.time) ?? 0, p.value));
  // v0.9.73 — p50 GAP'li: örnek olmayan (ya da 0 dönen) bucket'ta null →
  // çizgi tabana çakmaz, gerçek boşluk gösterir. Eski `?? 0` her boş
  // bucket'ı 0ms'e çekip sahte iniş-çıkış üretiyordu.
  const p50d: (number | null)[] = cPts.map(p => {
    const v = pMap.get(p.time);
    return v && v > 0 ? v : null;
  });
  const dt = t.length > 1 ? Math.round((t[1] - t[0]) / 60) : 1;
  // v0.10.322 (operatör: "Süre grafiği de çok zigzaglı olmasın") — medyan
  // çizgisi 5 kovalık merkezli hareketli ortalama (2 dk kovada 10 dk).
  // GAP'ler korunur (null merkez null kalır); kenarlarda mevcut komşularla.
  // Yoğun seride (≥20 kova) noktalar kapalı — nokta işaretleri zikzağın
  // görsel yarısıydı. Başlıktaki MEDIAN MAX ham kovadan okunur; etiket
  // yumuşatmayı söyler.
  const p50s = smoothCentered(p50d, P50_SMOOTH_WINDOW);
  // v0.10.268 (operatör: "bar gösterimleri Dynatrace gibi olabilir", mockup A
  // onayı) — SAYIM SOL eksende (çubuklar), MEDYAN yanıt süresi SAĞ eksende
  // (çizgi): Dynatrace "Trace count / Response Time (Median)" düzeni.
  // v0.9.843'ün Grafana takası bilinçli geri alındı; geri dönüş tek commit
  // (git revert). Seriler giriş span'ı (server/consumer) kapsamlı
  // (Traces.tsx şerit filtresi) — istek ≈ trace.
  const series: TimeChartSeries[] = [
    // v0.9.843 — bar'lar SAĞ eksende (span sayısı).
    { key: 'total', label: unit, data: total, color: 'var(--accent)', type: 'bar', axis: 'left' },
    { key: 'error', label: 'error ' + unit, data: err, color: statusColor('error'), type: 'bar', axis: 'left' },
    // v0.9.73 — kalın çizgi + nokta: seyrek p50 örnekleri artık okunur.
    // v0.9.843 — süre SOL eksende (Grafana düzeni).
    { key: 'p50', label: `response time (median, ${P50_SMOOTH_WINDOW}-kova ort.)`, data: p50s, color: 'var(--orange)', type: 'line', axis: 'right', width: 2, pointsShow: t.length < 20 },
  ];
  return { times: t, series, bucketMin: Math.max(1, dt) };
}


/** v0.10.322 — medyan çizgisi yumuşatma penceresi (kova sayısı, tek). */
export const P50_SMOOTH_WINDOW = 5;

/**
 * smoothCentered — merkezli hareketli ortalama, null-farkında. Merkez null
 * ise null (GAP korunur); değilse penceredeki null olmayan komşuların
 * ortalaması. window ≤ 1 → aynı dizi. SAF.
 */
export function smoothCentered(data: (number | null)[], window: number): (number | null)[] {
  if (window <= 1 || data.length < 3) return data;
  const half = Math.floor(window / 2);
  return data.map((v, i) => {
    if (v == null) return null;
    let sum = 0; let n = 0;
    for (let j = Math.max(0, i - half); j <= Math.min(data.length - 1, i + half); j++) {
      const x = data[j];
      if (x != null) { sum += x; n++; }
    }
    return n ? sum / n : v;
  });
}

// ── v0.10.323 (operatör, prod: db.statement ~ … filtresinde şerit boş) ──
// Şerit v0.10.268'den beri GİRİŞ span'ı (server/consumer) kapsamlı: istek ≈
// trace. Ama operatörün filtresi tablo tarafında TRACE düzeyinde uygulanır
// ("trace'in HERHANGİ bir span'i eşleşir"), şeritte ise aynı span'de AND'lenir.
// db.statement / messaging.* gibi alanlar giriş span'ında YAŞAMAZ → şerit
// "sıfır", tablo dolu. Çözüm dürüst ve ucuz: filtre giriş span'ında
// yaşamayan bir alanı hedefliyorsa (ya da serbest metin araması varsa) şerit
// EŞLEŞEN SPAN'LERİ sayar (kind kısıtı kalkar) ve bunu etikette söyler:
// birim "spans", medyan o span'lerin süresi (db.statement için = sorgunun
// kendi gecikmesi). Trace düzeyine çevirmek (aday id kümesi) prod ölçeğinde
// ikinci bir tam tarama olurdu; bilinçli yapılmadı.
export type StripScope = 'entry' | 'spans';

/** Giriş span'ında yaşayan anahtarlar / önekler — bunlar şeridi giriş kapsamında tutar. */
const ENTRY_KEYS = new Set(['service.name', 'name', 'kind', 'status_code', 'status', 'cluster', 'deployment.environment',
  'channel_code', 'function_code', 'function_id', 'span.kind', 'span.name']);
const ENTRY_PREFIXES = ['http.', 'url.', 'server.', 'k8s.', 'resource.', 'host.', 'service.', 'deployment.', 'telemetry.', 'process.', 'os.', 'container.', 'cloud.'];

export function isEntrySpanKey(key: string): boolean {
  const k = key.trim().toLowerCase();
  if (ENTRY_KEYS.has(k)) return true;
  return ENTRY_PREFIXES.some(p => k.startsWith(p));
}

/** stripScope — filtreler + serbest metin → şerit kapsamı. SAF. */
export function stripScope(filters: { k: string }[], search: string): StripScope {
  if (search.trim()) return 'spans';
  return filters.every(f => isEntrySpanKey(f.k)) ? 'entry' : 'spans';
}

/** volumeUnitFor — birim etiketi: spans kapsamında "spans", değilse eski kural. */
export function volumeUnitFor(serviceScoped: boolean, scope: StripScope): string {
  return scope === 'spans' ? 'spans' : volumeUnitLabel(serviceScoped);
}

/** volumeHint — başlık ipucu (title); kapsam neyi saydığını söyler. */
export function volumeHint(unit: string): string {
  return unit === 'spans'
    ? 'Filtre ya da arama giriş span\'ı dışındaki bir alanı hedefliyor (ör. db.statement): eşleşen SPAN\'ler sayılır, medyan o span\'lerin süresi. Tablo yine trace düzeyinde eşleşir.'
    : 'Giriş span\'leri (server/consumer) sayılır: servis seçiliyken istek = trace; servissiz pencerede her hop bir kez sayılır.';
}
