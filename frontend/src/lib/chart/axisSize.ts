// axisSize.ts — y ekseni OLUK GENİŞLİĞİ (v0.9.799, operatör-bildirimi:
// "eksende 00 req/s yazıyor, 100/200/300'ün baş rakamı kırpık").
//
// KÖK NEDEN, ölçülmüş: @grafana/ui'nin UPlotAxisBuilder'ı ekseni kendisi
// boyutlandırıyor (calculateAxisSize) ve en uzun tick etiketini
// `measureText(text, 12)` ile ölçüyor. O yardımcı fontu SABİT yazıyor:
// `400 12px 'Inter'`. Coremetry'de Inter YOK (uygulama fontu 'Red Hat
// Text'), yani canvas ölçümü tarayıcının varsayılan fontuna düşüyor —
// oysa uPlot ekseni Grafana temasının fontuyla ÇİZİYOR
// (`12px 'Inter','Helvetica','Arial',sans-serif` → Helvetica/Arial).
// Ölçülen font çizilenden dar olunca oluk bir karakter kadar eksik
// hesaplanıyor ve etiket soldan kırpılıyor. Grafana'da bu hata yok çünkü
// orada Inter GERÇEKTEN yüklü — hata bizim font seçimimizle doğuyor.
//
// ÇÖZÜM ölçümü DÜZELTMEK, etiketi kısaltmak değil: birimi tick'ten
// atmak (ikinci seçenek) v0.9.774'ün bilinçli kararını geri alırdı
// (eksen BİRİMLİ, tooltip/lejantla aynı kaynak) ve operatörün beklediği
// şey tam olarak "100 req/s tam görünsün".
//
// Bu dosya SAF: ölçüm dışında hiçbir yan etkisi yok, @grafana/* importu
// yok (decimals'ı çağıran verir — böylece testi node ortamında koşar).

/** UPLOT_AXIS_FONT_SIZE (@grafana/ui) — eksen yazı boyutu. */
export const AXIS_FONT_SIZE = 12;
/** UPlotAxisBuilder ticks.size varsayılanı. */
export const AXIS_TICK_SIZE = 4;
/** UPlotAxisBuilder gap varsayılanı. */
export const AXIS_GAP = 5;
/** Yuvarlama + alt-piksel payı: ölçüm doğru olsa bile 1-2 px kırpılma
 *  tek karakterlik bir okuma hatası üretir, tersi yalnız 4 px yer alır. */
export const AXIS_SAFETY_PX = 4;
/** Grafana'nın kendi tavanı (calculateAxisSize): oluk panelin %40'ından
 *  geniş olamaz — patlayan tek bir etiket grafiği yutmasın. */
export const AXIS_MAX_GUTTER_RATIO = 0.4;
/** Y_TICK_SPACING_NORMAL / _SMALL (@grafana/ui calculateSpace). */
export const AXIS_TICK_SPACING_PX = 30;
export const AXIS_TICK_SPACING_SMALL_PX = 15;
export const AXIS_SMALL_PLOT_PX = 150;

/** Kaba ölçüm için ortalama karakter genişliği (em). Basamak ve küçük
 *  harflerin üstünde BİLEREK: fazla tahmin birkaç piksel yer yer, eksik
 *  tahmin etiketi kırpar (düzeltmeye çalıştığımız kusurun ta kendisi). */
export const AXIS_EM_PER_CHAR = 0.62;
/** v0.10.191 (operatör-bildirimi, prod Windows/Edge: "0.15 cores" → "15 cores",
 *  "4.407 GiB" → "407 GiB"; lokalde ÜRETİLEMEDİ). Canvas ölçümü hangi
 *  sebeple olursa olsun (yedek font, DPR/ölçekleme, eksik font) çizilenden
 *  dar çıkarsa oluk yine kırpar. Ölçülen genişlik karakter tabanlı bir
 *  TABANIN altına inemez: 0.55 em ≈ Arial/Helvetica basamak genişliği —
 *  normal ölçümü büyütmez, yalnız gerçek-dışı küçük ölçümü yakalar. */
export const AXIS_EM_PER_CHAR_FLOOR = 0.55;

// ── Tick planı ────────────────────────────────────────────────────────────
//
// uPlot'un lineer eksen artışı 1/2/5 merdivenidir (varsayılan `incrs`);
// burada aynı merdiven kuruluyor çünkü oluğu HESAPLAMAK için etiketleri
// çizimden ÖNCE bilmemiz gerek. Birebir aynı kümeyi üretmek şart değil:
// güvenlik payı + "yalnız büyüyen" mandal (CorePanel) küçük sapmaları
// zaten yutuyor.

/** niceIncr — [1,2,5]×10^k merdiveninde, span/maxTicks'i karşılayan en
 *  küçük artış. span ≤ 0 → 0 (tek tick). */
export function niceIncr(span: number, maxTicks: number): number {
  if (!(span > 0) || !(maxTicks >= 1) || !isFinite(span)) return 0;
  const raw = span / maxTicks;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  for (const m of [1, 2, 5]) {
    if (raw <= m * mag) return m * mag;
  }
  return 10 * mag;
}

/** maxTicksFor — panel yüksekliğinden tick bütçesi (@grafana/ui
 *  calculateSpace'in y dalı: 150px altı panelde 15px, üstünde 30px). */
export function maxTicksFor(plotHeightPx: number): number {
  const h = Math.max(1, plotHeightPx);
  const spacing = h <= AXIS_SMALL_PLOT_PX ? AXIS_TICK_SPACING_SMALL_PX : AXIS_TICK_SPACING_PX;
  return Math.max(2, Math.floor(h / spacing));
}

export interface AxisTickPlan {
  /** Tick değerleri (artan). Boş veri → boş dizi. */
  ticks: number[];
  /** Seçilen artış — ondalık sayısı BUNDAN türetilir (Grafana ekseni de
   *  öyle yapıyor: guessDecimals(roundDecimals(tickIncr, 6))). */
  incr: number;
}

/** axisTickPlan — [min,max] + panel yüksekliği → çizilecek tick kümesi.
 *
 *  log dalı DECADE'lerdir (10^k): uPlot log ölçekte de öyle böler, ve
 *  lineer merdiveni log eksene uygulamak oluğu tümden yanlış hesaplardı.
 */
export function axisTickPlan(
  min: number, max: number, plotHeightPx: number, log = false,
): AxisTickPlan {
  if (!isFinite(min) || !isFinite(max)) return { ticks: [], incr: 0 };
  if (log) {
    const lo = Math.max(min, Number.MIN_VALUE);
    const hi = Math.max(max, lo);
    const k0 = Math.floor(Math.log10(lo));
    const k1 = Math.ceil(Math.log10(hi));
    const ticks: number[] = [];
    for (let k = k0; k <= k1 && ticks.length < 64; k++) ticks.push(Math.pow(10, k));
    return { ticks, incr: ticks[0] ?? 0 };
  }
  if (max === min) return { ticks: [min], incr: 0 };
  const incr = niceIncr(max - min, maxTicksFor(plotHeightPx));
  if (!incr) return { ticks: [min, max], incr: 0 };
  const ticks: number[] = [];
  const first = Math.ceil(min / incr) * incr;
  // Kayan nokta birikimini önlemek için indeksle çarpıyoruz (v += incr
  // 0.1'lik adımlarda 0.30000000000000004 üretir ve etiket uzar).
  const dec = decimalsForIncr(incr);
  for (let i = 0; i < 64; i++) {
    const v = first + i * incr;
    if (v > max + incr * 1e-9) break;
    ticks.push(roundToDecimals(v, dec));
  }
  return { ticks: ticks.length ? ticks : [min, max], incr };
}

/** decimalsForIncr — @grafana/data'nın guessDecimals(roundDecimals(n,6))
 *  ifadesinin bu dosyadaki saf ikizi. Ekseni biçimlendiren Grafana kodu
 *  ondalığı TAM böyle türetiyor; oluk hesabı ile çizilen etiket
 *  ayrışmasın diye aynı kural burada da yazılı. */
export function decimalsForIncr(incr: number): number {
  if (!isFinite(incr) || incr === 0) return 0;
  const r = roundToDecimals(Math.abs(incr), 6);
  return (('' + r).split('.')[1] || '').length;
}

/** Ondalık tavanı: bundan sonrası etiket değil gürültü. */
const MAX_TICK_DECIMALS = 6;

/** displayScaleOf — biçimlendiricinin uyguladığı ÖLÇEK: ham birimden
 *  gösterilen birime kaç kat (bytes→TiB'de 1024⁴, ns→ms'de 1e6).
 *  Biçimlendiriciye ölçek SORULMAZ, ondan geri OKUNUR: ham değeri
 *  biçimlet, metindeki sayıyı ayrıştır, oranı al. Böylece kural her
 *  birim ailesi için aynı gövdeden geçer — bytes/ms/req-s ayrı dal
 *  istemez. Ayrıştırılamayan metin (ör. "N/A") → 1, yani ölçek yok
 *  sayılır ve davranış bugünküyle bayt-bayt aynı kalır. */
export function displayScaleOf(raw: number, shownText: string): number {
  if (!isFinite(raw) || raw === 0) return 1;
  // parseFloat metnin BAŞINDAN okur; binlik ayraç taşıyan biçimler
  // ("1,024") kesilirdi — ayracı atarak okuyoruz.
  const shown = parseFloat(shownText.replace(/,/g, ''));
  if (!isFinite(shown) || shown === 0) return 1;
  const scale = Math.abs(raw / shown);
  return isFinite(scale) && scale > 0 ? scale : 1;
}

/** scaleRefTick — tick kümesinden ÖLÇEĞİ okumak için temsilci tick.
 *
 *  Biçimlendirici birimi DEĞER BAŞINA seçiyor: aynı eksen "994 ms" ile
 *  "1 s"i, ya da "1011 MiB" ile "1.01 GiB"i birlikte taşıyabiliyor.
 *  Ölçek en küçük tick'ten okunsaydı küçük birim (ms/MiB) bulunur, adım
 *  olduğundan büyük görünür ve ondalık eksik çıkardı — etiketler yine
 *  çakışırdı. Bu yüzden temsilci MUTLAK DEĞERCE EN BÜYÜK tick: eksenin
 *  hâkim birimini o belirliyor. Negatif tabanlı eksende de doğru çalışır.
 *  Boş kümede 0 → çağıran ölçeği 1 sayar (davranış değişmez). */
export function scaleRefTick(ticks: readonly number[]): number {
  let best = 0;
  for (const t of ticks) {
    if (isFinite(t) && Math.abs(t) > Math.abs(best)) best = t;
  }
  return best;
}

/** decimalsForScaledIncr — bir tick etiketinin, KOMŞUSUNDAN ayırt
 *  edilebilmesi için gereken ondalık sayısı.
 *
 *  v0.9.1368 (operatör-bildirimi: "Clusters altında memory hep aynı
 *  değeri gösteriyor" — dört tick de "6.85 TiB", çizgi ise oynuyor).
 *  KÖK NEDEN: ölçekli bir biçimlendiricide (bytes→TiB) ondalık sayısı
 *  DEĞERİN BÜYÜKLÜĞÜNDEN türetiliyordu (@grafana/data getDecimalsForValue,
 *  tavanı 2). 6.85 TiB'de 2 ondalık ≈ 10 GiB çözünürlük demek; eksen
 *  dar bir banda yakınsayınca her tick aynı dizeye yuvarlanıyor.
 *  @grafana/ui'nin kendi kuralı da kurtarmıyor: guessDecimals'ı HAM
 *  artışa (milyarlarca bayt) uyguluyor, 0 buluyor ve `undefined`
 *  geçiyor — yani eksen de tooltip'le aynı otomatik yola düşüyor.
 *
 *  KURAL: ondalık, değerin büyüklüğünden değil TICK ADIMINDAN gelir —
 *  ve adım GÖSTERİLEN birimde ölçülür. 2 GiB'lik adım TiB ekseninde
 *  0.00195'e küçülür, o da 3 ondalık ister.
 *
 *  Sonuç `decimalsForIncr` ile MAKSİMUMLANIR: ölçeksiz eksenlerde
 *  (scale=1) Grafana'nın kuralı aynen korunur, yeni terim yalnızca
 *  ölçek adımı küçülttüğünde devreye girer. Yani hassasiyet asla
 *  DÜŞMEZ — bu, v0.9.799'un req/s ekseni gibi çalışan panelleri
 *  regresyona sokmamanın garantisi. */
export function decimalsForScaledIncr(incr: number, scale: number): number {
  const base = decimalsForIncr(incr);
  if (!isFinite(incr) || incr === 0) return base;
  if (!isFinite(scale) || scale <= 0) return base;
  const scaledIncr = Math.abs(incr) / scale;
  if (!isFinite(scaledIncr) || scaledIncr === 0) return base;
  const need = Math.ceil(-Math.log10(scaledIncr));
  return Math.min(MAX_TICK_DECIMALS, Math.max(base, need, 0));
}

function roundToDecimals(v: number, dec: number): number {
  if (dec === 0) return Math.round(v);
  const f = Math.pow(10, dec);
  return Math.round(v * f) / f;
}

// ── Ölçüm ────────────────────────────────────────────────────────────────

/** estimateLabelWidthPx — canvas YOKKEN (test / SSR) kaba üst sınır. */
export function estimateLabelWidthPx(label: string, fontSizePx = AXIS_FONT_SIZE): number {
  return label.length * fontSizePx * AXIS_EM_PER_CHAR;
}

let ctx: CanvasRenderingContext2D | null | undefined;
const widthCache = new Map<string, number>();

/** measureLabelWidthPx — GERÇEKTEN çizilecek fontla ölçer. Grafana'nın
 *  measureText'i fontu sabit 'Inter' yazdığı için kullanılamıyor (dosya
 *  başı). Canvas yoksa kaba tahmine düşer — asla 0 döndürmez. */
export function measureLabelWidthPx(label: string, font: string): number {
  const key = `${font}\0${label}`;
  const hit = widthCache.get(key);
  if (hit != null) return hit;
  if (ctx === undefined) {
    ctx = typeof document === 'undefined'
      ? null
      : document.createElement('canvas').getContext('2d');
  }
  let w: number;
  if (!ctx) {
    w = estimateLabelWidthPx(label);
  } else {
    ctx.font = font;
    w = ctx.measureText(label).width;
    // jsdom gibi ölçmeyen ortamlar 0 döndürür — tahmine düş.
    if (!(w > 0)) w = estimateLabelWidthPx(label);
    // v0.10.191 — taban: ölçüm çizilenden dar çıkarsa (yedek font) kırpma.
    w = Math.max(w, labelWidthFloorPx(label, font));
  }
  // Sınırsız büyümesin: eksen etiketi kümesi küçüktür, 500 fazlasıyla yeter.
  if (widthCache.size > 500) widthCache.clear();
  widthCache.set(key, w);
  return w;
}

/** labelWidthFloorPx — font dizesindeki punto × karakter × 0.55 em; font
 *  punto taşımıyorsa AXIS_FONT_SIZE. SAF; axisSize.test.ts pinler. */
export function labelWidthFloorPx(label: string, font: string): number {
  const m = /(\d+(?:\.\d+)?)px/.exec(font);
  const px = m ? parseFloat(m[1]) : AXIS_FONT_SIZE;
  return label.length * px * AXIS_EM_PER_CHAR_FLOOR;
}

/** widestLabelPx — etiket kümesinin en geniş ölçüsü. */
export function widestLabelPx(labels: readonly string[], font: string): number {
  let w = 0;
  for (const l of labels) w = Math.max(w, measureLabelWidthPx(l, font));
  return w;
}

/** axisGutterPx — en geniş etiketten uPlot `axis.size` değeri.
 *
 *  Bileşimi @grafana/ui'nin calculateAxisSize'ı ile AYNI (tick çentiği +
 *  boşluk + metin) — tek fark ölçümün doğru fontla yapılması ve güvenlik
 *  payı. plotWidthPx verilirse Grafana'nın %40 tavanı da uygulanır.
 */
export function axisGutterPx(maxLabelWidthPx: number, plotWidthPx?: number): number {
  const raw = isFinite(maxLabelWidthPx) && maxLabelWidthPx > 0 ? maxLabelWidthPx : 0;
  const capped = plotWidthPx && plotWidthPx > 0
    ? Math.min(plotWidthPx * AXIS_MAX_GUTTER_RATIO, raw)
    : raw;
  return Math.ceil(AXIS_TICK_SIZE + AXIS_GAP + AXIS_SAFETY_PX + capped);
}

/** seriesExtent — çizilen matrisin y uçları (null/NaN atlanır).
 *  Zaman sütunu (0) DIŞARIDA: çağıran yalnız değer sütunlarını verir. */
export function seriesExtent(cols: ReadonlyArray<ReadonlyArray<number | null>>): [number, number] | null {
  let lo = Infinity;
  let hi = -Infinity;
  for (const col of cols) {
    for (const v of col) {
      if (v == null || !isFinite(v)) continue;
      if (v < lo) lo = v;
      if (v > hi) hi = v;
    }
  }
  return lo <= hi ? [lo, hi] : null;
}

/** paddedExtent — uPlot'un varsayılan %10 dolgusunun yaklaşığı. Veri
 *  tümüyle pozitifse taban 0'a MIHLANIR (uPlot'un soft-0 davranışı):
 *  yoksa hiç çizilmeyecek negatif etiketler için oluk açardık. */
export function paddedExtent([lo, hi]: [number, number], pad = 0.1): [number, number] {
  if (lo === hi) {
    const d = Math.abs(lo) * pad || 1;
    return [lo >= 0 ? Math.max(0, lo - d) : lo - d, hi + d];
  }
  const d = (hi - lo) * pad;
  return [lo >= 0 ? Math.max(0, lo - d) : lo - d, hi + d];
}
