// rootOpSeries — Service Overview "Response time" kartının operasyon
// kırılımı (v0.9.484, operatör onayı: "root spanler için multichart").
//
// NE: toplam görünüm (avg/P50/P95/P99) yerine, servisin en yoğun GİRİŞ
// operasyonlarının her biri için TEK bir P95 çizgisi. Aynı popülasyon
// (entryLatencyDSL — server + consumer), aynı pencere, aynı adım; değişen
// yalnız kırılım. Datadog'un "split by resource" refleksi: "servis yavaş"
// sinyalini "HANGİ endpoint yavaş"a çevirir.
//
// Bu dosya SAF: fetch yok, React yok. Sebebi tek — hizalama, sıralama,
// etiket ve "+N daha" türetimi testlenebilir kalsın (ChartCard'ın index
// eşlemesi hizasız seride sessizce yanlış zamana çizer, bkz. alignToUnion
// başlığı). Hook tarafı useRootOpLatency.ts.

import type { SpanMetricSeries, SpanMetricResult } from '@/lib/types';
import { alignToUnion } from '@/lib/chart/alignSeries';
import type { ChartLine } from './ChartCard';

// Kaç operasyon çizilir. 5: kartın yüksekliği ~150px ve lejant tablosu
// varsayılan kapalı (v0.9.483) — daha fazlası okunmuyor, ayırt edilebilir
// token sayısı da 5'te bitiyor (aşağı bkz. ROOT_OP_COLORS).
export const ROOT_OP_TOP_N = 5;

// Çizgi renkleri — YALNIZ token (globals.css'te üç temada da tanımlı:
// --accent/--orange/--teal/--purple/--ok). Hex palet (lib/chartFmt
// seriesColor) bilerek kullanılmadı: bu kart temaya tepki veren
// var() renkleriyle çiziliyor (OverviewChart resolveVar ile çözer).
// --err DIŞARIDA: bu kartta kırmızı "hata" demek (failure-rate paneli,
// P99 çizgisi) — bir operasyonun adına rastgele kırmızı düşmesi yanlış
// alarm okuması yaratır.
export const ROOT_OP_COLORS = [
  'var(--accent)',
  'var(--orange)',
  'var(--teal)',
  'var(--purple)',
  'var(--ok)',
] as const;

/** Renk ataması: sıraya göre (en büyük alan ilk). Kap içinde ASLA tekrar yok. */
export function rootOpColorAt(index: number): string {
  return ROOT_OP_COLORS[((index % ROOT_OP_COLORS.length) + ROOT_OP_COLORS.length) % ROOT_OP_COLORS.length];
}

// groupBy=name tek boyutlu; yine de groupKey dizisi geliyor. İlk boş
// olmayan eleman = operasyon adı. Hepsi boşsa "(adsız)" — CH'de name=''
// olan span'ler var (enstrümantasyon hatası) ve boş etiket lejantta
// görünmez satır olurdu.
export function rootOpName(groupKey: readonly string[] | undefined): string {
  const hit = (groupKey ?? []).find(k => k != null && k !== '');
  return hit ?? '(adsız)';
}

/** Lejant etiketi — "opName · P95". Kart başlığı hangi metrik olduğunu
 *  söylemiyor (toplam görünümde 4 çizgi vardı), bu yüzden her çizgi kendi
 *  agg'ini taşır. */
export function rootOpLineLabel(groupKey: readonly string[] | undefined): string {
  return `${rootOpName(groupKey)} · P95`;
}

/** Alan (|değer| toplamı) — PanelStack'in top-N ölçütünün aynısı. P95
 *  serisinde alan ≈ "sürekli ve yüksek gecikme", yani operatörün bu kartı
 *  açarken aradığı şey. */
export function rootOpArea(s: SpanMetricSeries): number {
  return (s.points ?? []).reduce((a, p) => a + Math.abs(p.value ?? 0), 0);
}

/** Alana göre azalan sırala + kap. Eşitlikte ada göre (deterministik
 *  render: aynı veri → aynı renk sırası). */
export function rankRootOps(series: readonly SpanMetricSeries[], cap = ROOT_OP_TOP_N): SpanMetricSeries[] {
  return [...series]
    .map(s => ({ s, area: rootOpArea(s) }))
    .sort((a, b) => (b.area - a.area) || rootOpName(a.s.groupKey).localeCompare(rootOpName(b.s.groupKey)))
    .slice(0, Math.max(0, cap))
    .map(x => x.s);
}

// Dürüstlük notu (v0.9.458 emsali). İki AYRI yalan var, ikisi de söylenir:
//   • more        — evrende daha çok operasyon var, çizilen ilk N'i (alan bazlı).
//                   total, zarfın totalSeries'i (sunucu top-N kırpması ÖNCESİ);
//                   yoksa gelen seri sayısı.
//   • rowsCapped  — sorgu 50k satır tavanına çarptı, liste ALFABETİK kesildi:
//                   "N daha" bile gerçek evreni bilmiyor.
export function rootOpMoreNote(total: number, shown: number, rowsCapped = false): string | null {
  const more = Math.max(0, total - shown);
  const base = more > 0 ? `+${more} operasyon daha (alan bazlı kırpıldı)` : null;
  if (!rowsCapped) return base;
  const capped = '⚠ satır tavanı doldu — liste eksik olabilir';
  return base ? `${base} · ${capped}` : capped;
}

export interface RootOpLines {
  /** ChartCard'a verilecek çizgiler; hepsi AYNI union zaman eksenine hizalı. */
  lines: ChartLine[];
  /** Çizilen operasyon sayısı. */
  shown: number;
  /** Kırpma + tavan notu (yoksa null). */
  note: string | null;
}

// buildRootOpLines — zarf → ChartCard çizgileri.
//
// Hizalama: her operasyon AYRI bir GROUP BY satır kümesi, bir bucket'ta hiç
// span üretmemiş olabilir (zero-fill yok). ChartCard değerleri İNDEKSLE
// eşler, dolayısıyla ham points'i vermek çizgiyi yanlış zamana kaydırırdı
// (RuntimeCharts'ta yaşanan sınıf). alignToUnion ile tek eksen + eksik
// bucket'ta null (boşluk) → ChartLine.values (v0.9.483 kaçış kapısı; points
// tipi number olduğu için boşluk taşıyamıyor).
//
// Zaman ekseni: ChartCard `times`i İLK boş-olmayan series'ten okur. Tüm
// çizgilere AYNI eksen nesnesi (referansla paylaşılan) verilir — sıralama
// değişse de eksen sabit kalır.
export function buildRootOpLines(
  res: SpanMetricResult | null | undefined,
  cap = ROOT_OP_TOP_N,
): RootOpLines {
  const all = res?.series ?? [];
  const top = rankRootOps(all, cap);
  if (top.length === 0) {
    return { lines: [], shown: 0, note: rootOpMoreNote(0, 0, res?.rowsCapped ?? false) };
  }
  const { times, cols } = alignToUnion(top.map(s => s.points ?? []));
  // Eksen taşıyıcısı: değer okunmaz (her çizgide `values` var), yalnız
  // ChartCard'ın x eksenini türettiği yer.
  const axis: SpanMetricSeries[] = [{ groupKey: [], points: times.map(t => ({ time: t, value: 0 })) }];
  const lines: ChartLine[] = top.map((s, i) => ({
    series: axis,
    color: rootOpColorAt(i),
    label: rootOpLineLabel(s.groupKey),
    values: cols[i],
  }));
  const total = res?.totalSeries ?? all.length;
  return { lines, shown: top.length, note: rootOpMoreNote(total, top.length, res?.rowsCapped ?? false) };
}

// ── v0.9.728 — CorePanelMulti (v2) projeksiyonu ─────────────────────────
//
// ChartCard yolundan farkı: union hizalama YOK — CorePanel'in frame
// birleşimi (framesToAligned) zaman eksenini kendisi kurar, eksik bucket
// null olur (sıkı doktrin). Renk de verilmez: CorePanel 'data' rolünde
// etikete deterministik palet uygular (seriesRole sözleşmesi — aynı
// operasyon her yüzeyde aynı renk; eski index-palet yerine bilinçli).
export interface RootOpItem {
  name: string;
  role: 'data';
  series: SpanMetricSeries[];
}

export interface RootOpItems {
  items: RootOpItem[];
  note: string | null;
}

export function buildRootOpItems(
  res: SpanMetricResult | null | undefined,
  cap = ROOT_OP_TOP_N,
): RootOpItems {
  const all = res?.series ?? [];
  const top = rankRootOps(all, cap);
  const total = res?.totalSeries ?? all.length;
  return {
    items: top.map(s => ({
      name: rootOpLineLabel(s.groupKey),
      role: 'data' as const,
      series: [s],
    })),
    note: rootOpMoreNote(total, top.length, res?.rowsCapped ?? false),
  };
}
