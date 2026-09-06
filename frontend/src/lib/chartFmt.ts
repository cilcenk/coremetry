// Chart-specific formatters and palette. Centralised so every
// chart on the app reads the same way — same number rendered
// the same, same series rendered the same colour. The polish
// gap between "amateur" and "Datadog/Grafana" is mostly here:
// raw numbers like "12500" and one-off colours via hashColor
// look home-rolled; "12.5k rps" with a curated 10-colour
// palette looks like a product.

// fmtSmart — turns a raw value into a human-readable label,
// unit-aware. Axis ticks + tooltip values + KPI tiles all
// share this so e.g. P99 latency reads "234ms" everywhere
// rather than "234.567" in one place and "234ms" in another.
export function fmtSmart(v: number | null | undefined, unit?: string): string {
  if (v == null || !isFinite(v)) return '—';
  const u = (unit || '').trim();

  // Time — auto-promote ms → s → m so a 5-minute chart axis
  // doesn't read "300000ms".
  if (u === 'ms') {
    const abs = Math.abs(v);
    if (abs >= 60_000) return trim(v / 60_000) + 'm';
    if (abs >= 1_000)  return trim(v / 1_000)  + 's';
    if (abs >= 10)     return v.toFixed(0)     + 'ms';
    if (abs >= 1)      return v.toFixed(1)     + 'ms';
    return v.toFixed(2) + 'ms';
  }

  if (u === 's') {
    const abs = Math.abs(v);
    if (abs >= 60) return trim(v / 60) + 'm';
    if (abs >= 1)  return trim(v) + 's';
    return (v * 1000).toFixed(0) + 'ms';
  }

  if (u === '%') {
    const abs = Math.abs(v);
    if (abs >= 100) return v.toFixed(0) + '%';
    if (abs >= 10)  return v.toFixed(1) + '%';
    return v.toFixed(2) + '%';
  }

  // Bytes — IEC-style (1k = 1000) so it matches CH's
  // formatReadableSize defaults the operator already sees in
  // the cardinality dashboard.
  if (u === 'B' || u === 'bytes') return fmtBytes(v);

  // Throughput-like — count + " unit"
  if (/^(rps|qps|eps|msg\/s|\/s|ops|ops\/s)$/i.test(u)) {
    return fmtCount(v) + ' ' + u;
  }

  // Default — count + optional unit
  return fmtCount(v) + (u ? ' ' + u : '');
}

// fmtAxisTick — the compact y-axis tick label for the uPlot charts (Grafana-
// parity #3). Grafana puts a smart, unit-aware, SI-abbreviated value on the
// axis; we do the same but tuned for our NARROW gutter (OVC 34px / TC 38px):
//   • 0 → a clean "0" (bare fmtSmart would print "0.00ms" / "0.00%").
//   • short units (ms/s/%/B) ride the shared fmtSmart, so a tick reads the
//     same as the hover tooltip ("125ms", "12.5%", "1.5 kB").
//   • counts + WIDE throughput units ("req/s", "ops/s") → SI number ONLY.
//     Appending "1.2k req/s" would overrun the fixed gutter and force a wider
//     axis — a LAYOUT change this format-only pass must not make; the unit
//     stays in the chart title + tooltip. Whole counts stay whole ("5", not
//     the count formatter's "5.00") so integer axes don't regress vs kfmt.
export function fmtAxisTick(v: number, unit?: string): string {
  if (v === 0) return '0';
  const u = (unit || '').trim();
  if (u === 'ms' || u === 's' || u === '%' || u === 'B' || u === 'bytes') {
    return fmtSmart(v, u);
  }
  const abs = Math.abs(v);
  if (abs >= 1e12) return trim(v / 1e12) + 'T';
  if (abs >= 1e9)  return trim(v / 1e9)  + 'G';
  if (abs >= 1e6)  return trim(v / 1e6)  + 'M';
  if (abs >= 1e3)  return trim(v / 1e3)  + 'k';
  return Number.isInteger(v) ? String(v) : String(+v.toFixed(2));
}

// fmtCount — k / M / G / T suffix. Two-decimal precision under
// the next decade so the eye reads "1.23k" not "1230".
function fmtCount(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e12) return trim(v / 1e12) + 'T';
  if (abs >= 1e9)  return trim(v / 1e9)  + 'G';
  if (abs >= 1e6)  return trim(v / 1e6)  + 'M';
  if (abs >= 1e3)  return trim(v / 1e3)  + 'k';
  if (abs >= 100)  return v.toFixed(0);
  if (abs >= 10)   return v.toFixed(1);
  if (abs >= 1)    return v.toFixed(2);
  if (abs > 0)     return v.toFixed(3);
  return '0';
}

function fmtBytes(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e12) return trim(v / 1e12) + ' TB';
  if (abs >= 1e9)  return trim(v / 1e9)  + ' GB';
  if (abs >= 1e6)  return trim(v / 1e6)  + ' MB';
  if (abs >= 1e3)  return trim(v / 1e3)  + ' kB';
  return v.toFixed(0) + ' B';
}

// trim — drop trailing ".00" / ".50" → "0.5" so "1.00k"
// reads as "1k" but "1.23k" stays as is.
function trim(v: number): string {
  // Use 2 decimals if the value is < 10 in its own decade,
  // 1 decimal if 10-99, 0 if ≥100. Rounded toFixed first to
  // avoid float-noise like "1.2300000000000001".
  const abs = Math.abs(v);
  let s: string;
  if (abs >= 100)     s = v.toFixed(0);
  else if (abs >= 10) s = v.toFixed(1);
  else                s = v.toFixed(2);
  // Strip trailing zeros after decimal point: "1.20" → "1.2",
  // "1.00" → "1".
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s;
}

// ── Series colour palette ───────────────────────────────────
//
// Curated 10-colour palette chosen for:
//   • Distinguishability — every pair has a clear hue gap.
//   • Light/dark parity — saturation tuned so nothing reads
//     fluorescent on white or muddy on dark.
//   • Stability — same series name lands on the same colour
//     across every chart in the app, so an operator who learns
//     "frontend = blue" carries that mental model around.
//
// Order is intentional — the first few are the most readable
// when only a couple of series are on screen (Datadog blue,
// orange, green is the "primary trio" most APM tools converge
// on).
// v0.10.510 (dış skill denetimi D7, mockup onaylı) — SEKİZ yuvalı, TEMA
// BAŞINA adımlanmış, validator'dan geçmiş seri paleti. Eski on sabit renk
// tema tokenlarıyla çakışıyordu (koyu temada seri yeşili = --ok, altın =
// --warn), renk körlüğünde turuncu↔yeşil ΔE 3.0 idi ve ışıklık bandı
// dışındaydı. Yuva 1 = Coremetry vurgusu; durum renkleri (--ok/--warn/
// --err) paletin DIŞINDA. Koyu yüzeyde beş kontrol PASS; açık yüzeylerde
// üç rengin kontrastı 3:1 altı (validator WARN) — karşılığı görünür
// lejant/tooltip (≥2 serili her grafik taşır). Koyulaştırılmış varyant
// denendi: renk körlüğü ayrımı 8'in altına iniyordu, reddedildi.
export type ChartTheme = 'dark' | 'light' | 'redhat';
export const SERIES_PALETTES: Record<ChartTheme, readonly string[]> = {
  dark:   ['#388bfd', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'],
  light:  ['#0969da', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#4a3aa7', '#e34948'],
  redhat: ['#0066cc', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#4a3aa7', '#e34948'],
};
export const SERIES_SLOTS = 8;

// chartTheme — <html data-theme>'den; yalnız light/redhat token setini
// değiştirir (globals.css), yokluk/dark = koyu tokenlar. Canvas var()
// okuyamadığı için palet burada çözülür; tema değişince grafikler
// useThemeTick ile yeniden kurulur.
export function chartTheme(): ChartTheme {
  if (typeof document === 'undefined') return 'dark';
  const t = document.documentElement.getAttribute('data-theme');
  return t === 'light' || t === 'redhat' ? t : 'dark';
}

export function seriesPalette(theme: ChartTheme = chartTheme()): readonly string[] {
  return SERIES_PALETTES[theme];
}

function fnv1a(label: string): number {
  let h = 2166136261;
  for (let i = 0; i < label.length; i++) {
    h ^= label.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return Math.abs(h);
}

// preferredSlot — adın tercih ettiği yuva (hash mod 8): aynı ad her
// panelde aynı yuvayı İSTER; panel dolduysa sıradaki boş yuvayı alır.
export function preferredSlot(label: string): number {
  return fnv1a(label) % SERIES_SLOTS;
}

// seriesColor — panel bağlamı OLMAYAN yerler için (flame düğümü, komşu
// servis, harita halkası): tercih edilen yuvanın rengi, tema-farkında.
// Deterministic so the same label always hits the same colour.
export function seriesColor(label: string): string {
  return seriesPalette()[preferredSlot(label)];
}

// assignSeriesSlots — v0.10.510 SAF: bir panelin SIRALI seri etiketlerine
// yuva atar. Kural: her etiket tercih ettiği yuvayı (hash mod 8) alır;
// doluysa sıradaki boş yuva. `pinned` (önceki atama) verilirse o
// etiketler önce ve aynen yerleşir — süzgeç seri sayısını değiştirdiğinde
// kalanlar yeniden BOYANMAZ (renk varlığı izler, sırayı değil). Sekizden
// fazla seri: dokuzuncudan itibaren yuvalar dolaşır (çakışma kaçınılmaz;
// çağıran "diğer"e katlamalı — LogsHistogram bunu yapar).
export function assignSeriesSlots(labels: readonly string[], pinned?: ReadonlyMap<string, number>): Map<string, number> {
  const out = new Map<string, number>();
  const taken = new Set<number>();
  const place = (label: string, want: number) => {
    let s = want % SERIES_SLOTS;
    for (let k = 0; k < SERIES_SLOTS && taken.has(s); k++) s = (s + 1) % SERIES_SLOTS;
    if (taken.size >= SERIES_SLOTS) s = want % SERIES_SLOTS; // dolaşım: 9. seri
    out.set(label, s);
    taken.add(s);
  };
  if (pinned) {
    for (const l of labels) {
      const p = pinned.get(l);
      if (p !== undefined && !out.has(l) && !taken.has(p % SERIES_SLOTS)) place(l, p);
    }
  }
  for (const l of labels) {
    if (!out.has(l)) place(l, preferredSlot(l));
  }
  return out;
}

// seriesColorsFor — panel bağlamlı renkler: etiket → renk (tema-farkında).
export function seriesColorsFor(labels: readonly string[], pinned?: ReadonlyMap<string, number>, theme: ChartTheme = chartTheme()): Map<string, string> {
  const pal = seriesPalette(theme);
  const slots = assignSeriesSlots(labels, pinned);
  const out = new Map<string, string>();
  for (const [l, s] of slots) out.set(l, pal[s]);
  return out;
}

// fmtXTicks — shared time-axis label formatter for uPlot charts. Takes the
// SPLITS array (the tick timestamps uPlot picked) and formats them compactly:
//   • single-day spans → HH:MM on every tick
//   • multi-day spans  → HH:MM, with the MM-DD prefix ONLY on the first tick
//     of each new day
// v0.8.58 refines the v0.5.380 fix: that one stamped MM-DD HH:MM on EVERY
// multi-day label, so on a 2-day+ range the wide labels still collided
// horizontally (operator-reported: "metriklerin zamanları üst üste biniyor").
// Showing the date only when the day changes keeps every label narrow (mostly
// HH:MM) while still marking day boundaries — pair it with a min `space` on the
// axis so uPlot also thins the tick count to the available width.
export function fmtXTicks(splits: number[]): string[] {
  if (splits.length === 0) return [];
  const first = splits[0];
  const last = splits[splits.length - 1];
  const sameDay = splits.length > 1
    ? new Date(first * 1000).toDateString() === new Date(last * 1000).toDateString()
    : true;
  // v0.9.329 — resolution follows the TICK SPACING, not the total span.
  //
  // This formatter only ever emitted HH:MM, so any axis whose ticks sit less
  // than a minute apart rendered every label identically. Operator screenshot
  // from prod: an exception group living 05:30:18 → 05:30:49 — 31 seconds —
  // drew SIX ticks all reading "05:30". The chart was not wrong, it was
  // unreadable, and it looked like a rendering bug.
  //
  // Keyed on spacing rather than span because that is exactly the condition
  // under which HH:MM stops being able to distinguish two ticks: a 10-minute
  // window with 2-minute ticks is still perfectly legible as HH:MM and must
  // not grow a noisier ":SS" suffix.
  const stepSec = splits.length > 1
    ? Math.min(...splits.slice(1).map((v, i) => Math.abs(v - splits[i])))
    : Infinity;
  const withSec = stepSec < 60;
  const withMs = stepSec < 1;
  return splits.map((s, i) => {
    const d = new Date(s * 1000);
    const hh = String(d.getHours()).padStart(2, '0');
    const mi = String(d.getMinutes()).padStart(2, '0');
    let hm = `${hh}:${mi}`;
    if (withSec) {
      hm += `:${String(d.getSeconds()).padStart(2, '0')}`;
      if (withMs) hm += `.${String(d.getMilliseconds()).padStart(3, '0')}`;
    }
    if (sameDay) return hm;
    const prev = i > 0 ? new Date(splits[i - 1] * 1000) : null;
    const dayChanged = !prev || prev.toDateString() !== d.toDateString();
    if (!dayChanged) return hm;
    const mm = String(d.getMonth() + 1).padStart(2, '0');
    const dd = String(d.getDate()).padStart(2, '0');
    // v0.9.329 — reuse `hm` rather than re-deriving HH:MM here: the
    // day-prefixed branch used to rebuild the time from scratch, so it
    // silently dropped the seconds the branch above had just added.
    return `${mm}-${dd} ${hm}`;
  });
}

// fmtTooltipTime — hover tooltip'inin ZAMAN satırı (v0.9.483, operatör:
// "çıkan hover'da ay gün saat yok"). Grafiğin üstündeki tooltip yalnız
// "23:12" yazıyordu; 24 saatlik / çok günlü bir pencerede hangi GÜNE
// bakıldığı tooltip'ten okunamıyordu (x ekseninde gün ancak gün DEĞİŞTİĞİ
// tick'te görünüyor — fmtXTicks). Tarih artık her zaman orada:
//
//   "31.07.2026 23:12"    → gün.ay.yıl + HH:MM
//   "31.07.2026 23:12:05"  → tick aralığı < 60sn ise saniye de eklenir
//
// Saniye kuralı fmtXTicks'in v0.9.329'daki kuralının AYNISI (adım < 60sn):
// eksen ile tooltip aynı çözünürlükte konuşsun; dakikalık bir grafiğe
// gürültülü ":00" eki gelmesin. stepSec verilmezse saniye yok.
//
// Biçim gün.ay.yıl (31.07.2026) — TR okuma sırası; log TIME kolonuyla aynı
// düzen. v0.9.488 (operatör: "burada ay gün yıl yazmıyor") yıl da eklendi —
// tooltip bir ETİKET, ama yıl olmadan geçen yılın penceresi bugünmüş gibi
// okunabiliyordu.
export function fmtTooltipTime(tSec: number, stepSec?: number | null): string {
  const d = new Date(tSec * 1000);
  const p2 = (n: number) => String(n).padStart(2, '0');
  const head = `${p2(d.getDate())}.${p2(d.getMonth() + 1)}.${d.getFullYear()} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
  return stepSec != null && isFinite(stepSec) && stepSec < 60
    ? `${head}:${p2(d.getSeconds())}`
    : head;
}

// niceTickValues — given a min / max range, pick "round"
// gridline values an operator's eye can read. uPlot picks
// reasonable defaults but for ms / % / bytes the auto-picker
// produces awkward fractions; we override with snap-to-decade.
export function niceTickValues(min: number, max: number, target = 6): number[] {
  if (!isFinite(min) || !isFinite(max) || max <= min) return [];
  const range = max - min;
  // Pick the largest "nice" step that fits target ticks.
  const rough = range / target;
  const mag = Math.pow(10, Math.floor(Math.log10(rough)));
  // Round to the nearest 1 / 2 / 5 of that magnitude.
  const norm = rough / mag;
  const step = (norm < 1.5 ? 1 : norm < 3 ? 2 : norm < 7 ? 5 : 10) * mag;
  const ticks: number[] = [];
  const start = Math.ceil(min / step) * step;
  for (let v = start; v <= max + step / 2; v += step) {
    ticks.push(v);
  }
  return ticks;
}
