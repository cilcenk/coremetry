import type uPlot from 'uplot';
import { resolveVar } from './resolveVar';

// overlays.ts (Grafana-parite M3) — paylaşımlı ÇİZİM çekirdeği: y-threshold
// çizgileri + x-ekseni zaman-bölgesi (problem/anomali penceresi) gölgeleme.
//
// Motor sözleşmesi (engine.ts): options/hooks PRESET'te kalır — burada yalnız
// draw-hook içinden çağrılan saf çizim fonksiyonları + saf piksel yardımcıları
// yaşar. Dört preset (TimeSeriesPanel / MultiLineChart / TimeChart /
// OverviewChart) kendi draw hook'undan delege eder:
//   • drawThresholds — TSP ~429-460 ve MLC ~593-640'taki birebir KOPYA
//     threshold bloğunun TEK kaynağı (çizgi + ihlal bandı + sağ-kenar etiket;
//     görsel birebir korunur — bandAlpha preset'ten gelir: TSP 0x14/255,
//     MLC/OVC/TC 0.07).
//   • drawTimeRegions — YENİ: {fromSec,toSec} bölgelerini u.valToPos ile
//     arka-plan gölgesi + üst şerit + küçük etiket olarak çizer (mockup:
//     "▮ P1 problem penceresi"). valToPos CANLI ölçeği okuduğundan zoom'la
//     doğru konumlanır — EventMarkers'ın (motordan bağımsız DOM overlay,
//     sayfa from/to %-konumu, ZOOM-KÖR) bilinen borcu burada tekrarlanmaz;
//     EventMarkers ayrı borç olarak duruyor, bu dosya ona dokunmaz.
//
// Renkler: bölge renkleri var(--token) olarak taşınır ve ÇİZİM anında
// resolveVar ile çözülür (tema-canlı); threshold renklerini preset kendi
// mevcut zamanlamasıyla çözer (TSP draw-anı, MLC build-anı) ve buraya
// çözülmüş verir — SIFIR davranış değişikliği.

// ── Tüketici tipleri ────────────────────────────────────────────────────────

// ChartThreshold — OVC/TC'nin YENİ `thresholds` prop tipi (TSP'nin
// TSThreshold'u ile aynı şekil; MLC severity-tabanlı Threshold'unu korur).
export interface ChartThreshold {
  value: number;
  label?: string;
  color?: string; // CSS rengi/token (var(--warn) default'u preset'te)
}

// ChartTimeRegion — 4 preset'in ortak `regions` prop tipi. fromSec/toSec unix
// SANİYE (uPlot x ekseni ile aynı birim).
export interface ChartTimeRegion {
  fromSec: number;
  toSec: number;
  color?: string; // CSS token; default var(--err) (problem kırmızısı)
  label?: string; // ör. 'P1' / 'CRITICAL' / 'OPEN'
  /** v0.10.180 — tüketici anahtarı (anomali id); tıklamada geri döner. Deploy ▼'de yok. */
  id?: string;
  /** v0.10.182 — GERÇEK bitiş (sn). toSec çizim için şişirilebilir (min genişlik);
   *  tooltip «son/süre» bundan okur. Yoksa anlık olay (deploy ▼): son/süre yazılmaz. */
  endSec?: number;
}

// ResolvedThreshold — drawThresholds girdisi: renk canvas-hazır (preset çözdü).
export interface ResolvedThreshold {
  value: number;
  label?: string;
  color: string;
}

// ── Saf yardımcılar (vitest: overlays.test.ts) ─────────────────────────────

// thresholdVisible — eşik canlı y-ölçeğinin içinde mi (dışındaysa çizilmez;
// TSP/MLC'deki `value < yMin || value > yMax → continue` kuralının aynısı).
export function thresholdVisible(value: number, yMin: number, yMax: number): boolean {
  return value >= yMin && value <= yMax;
}

// clampRegion — bölgeyi canlı x-penceresine kırpar; pencereyle kesişmiyorsa /
// ters-bozuksa null (çizilmez). Zoom sonrası kısmi görünürlük buradan çıkar.
export function clampRegion(
  fromSec: number, toSec: number, xMin: number, xMax: number,
): { from: number; to: number } | null {
  if (!isFinite(fromSec) || !isFinite(toSec) || toSec <= fromSec) return null;
  const from = Math.max(fromSec, xMin);
  const to = Math.min(toSec, xMax);
  if (to <= from) return null; // pencere dışı
  return { from, to };
}

// fitLabel — etiketi availPx'e sığdır: sığıyorsa aynen, sığmıyorsa '…' ile
// kısalt, tek karakter+… bile sığmıyorsa '' (hiç çizme — dar bölgede etiket
// taşması yerine sessizlik). measure çağıranın ctx.measureText'i.
export function fitLabel(
  label: string, availPx: number, measure: (s: string) => number,
): string {
  if (!label || !(availPx > 0)) return '';
  if (measure(label) <= availPx) return label;
  for (let n = label.length - 1; n >= 1; n--) {
    const cand = label.slice(0, n).trimEnd() + '…';
    if (measure(cand) <= availPx) return cand;
  }
  return '';
}

// ── Çizim çekirdekleri (draw hook içinden; canvas gerektirir) ──────────────

// drawThresholds — yatay kesikli eşik çizgisi + üstünde ihlal bandı + sağ
// kenarda etiket. TSP/MLC kopyalarının birebiri: font 10px ui-monospace,
// lineWidth 1.2, dash [6,4], etiket sağdan 4px içeride ve çizginin 4px
// üstünde. bandAlpha: MLC 0.07 (globalAlpha yolu); TSP 0x14/255 (eski
// hex+'14' dolgusunun alfa eşdeğeri — hex olmayan degenerate renkte eski
// kod sabit ambere düşerdi, burada rengin kendisi kullanılır; mevcut tüm
// çağıranlar token→hex çözdüğünden görünür davranış birebir).
export function drawThresholds(
  u: uPlot,
  thresholds: ResolvedThreshold[],
  opts?: { bandAlpha?: number; scaleKey?: string },
): void {
  if (thresholds.length === 0) return;
  const scaleKey = opts?.scaleKey ?? 'y';
  const bandAlpha = opts?.bandAlpha ?? 0.07;
  const yMin = u.scales[scaleKey]?.min ?? 0;
  const yMax = u.scales[scaleKey]?.max ?? 0;
  const ctx = u.ctx;
  ctx.save();
  ctx.font = '10px ui-monospace, monospace';
  for (const th of thresholds) {
    if (!thresholdVisible(th.value, yMin, yMax)) continue;
    const y = u.valToPos(th.value, scaleKey, true);
    // İhlal bandı — çizginin ÜSTÜ hafifçe eşik renginde (canvas fillStyle
    // color-mix bilmez; globalAlpha hex+alfa dolgusuyla aynı kompoziti verir).
    ctx.globalAlpha = bandAlpha;
    ctx.fillStyle = th.color;
    ctx.fillRect(u.bbox.left, u.bbox.top, u.bbox.width, y - u.bbox.top);
    ctx.globalAlpha = 1;
    // Çizginin kendisi.
    ctx.strokeStyle = th.color;
    ctx.fillStyle = th.color;
    ctx.lineWidth = 1.2;
    ctx.setLineDash([6, 4]);
    ctx.beginPath();
    ctx.moveTo(u.bbox.left, y);
    ctx.lineTo(u.bbox.left + u.bbox.width, y);
    ctx.stroke();
    // Sağ kenarda etiket.
    if (th.label) {
      ctx.setLineDash([]);
      const labelW = ctx.measureText(th.label).width;
      ctx.fillText(th.label, u.bbox.left + u.bbox.width - labelW - 4, y - 4);
    }
  }
  ctx.restore();
}

// Bölge görseli (mockup chart-parity-mock.html): tüm yükseklikte %7 gölge +
// üstte 3px %55 şerit + şeridin altında küçük "▮ label". Yeni çizim olduğu
// için OVC/TC deploy-plugin emsaliyle dpr-ölçekli (retina'da mockup kalınlığı).
const REGION_FILL_ALPHA = 0.07;
const REGION_STRIP_ALPHA = 0.55;
const REGION_STRIP_H = 3;

// drawTimeRegions — {fromSec,toSec} bölgelerini canlı x-ölçeğine göre çizer.
// valToPos canlı ölçeği okur → drag-zoom / kontrollü zoomWindow'da bölge
// DOĞRU konumda kalır (EventMarkers'ın zoom-körlüğünün tersine). Threshold /
// deploy overlay'lerinden ÖNCE çağrılır ki gölge en arkada kalsın.
//
// v0.10.164 — `xUnit`: bölgeler unix SANİYE, ama CorePanel'in x ölçeği MS
// (xRange.ts timeScaleRange, Grafana frame sözleşmesi). Dönüşümsüz çağrıda
// clampRegion sn'yi ms penceresiyle kesiştirip null döndürüyordu — v0.9.945'te
// RED üçlüsü CorePanel'e taşındığından beri deploy ▼ / Problem / anomali
// bantları o panellerde HİÇ çizilmiyordu (kimse bakmadı: bant yokluğu
// sessiz). Saniye-eksenli eski motorlar (TimeChart / TimeSeriesPanel /
// OverviewChart) varsayılan 1 ile aynen. Saf yardımcı regionsToScale test
// edilir; CorePanel'in 1000 geçtiğini corePanelContracts.test.ts pinler.
export function regionsToScale(regions: ChartTimeRegion[], xUnit: number): ChartTimeRegion[] {
  return xUnit === 1 ? regions : regions.map(r => ({ ...r, fromSec: r.fromSec * xUnit, toSec: r.toSec * xUnit }));
}

// assignLanes — v0.10.166 (etüt «anomali işaretleri» dilim 2, şerit parçası):
// zamanda ÇAKIŞAN bölgelerin şerit+etiketleri aynı piksele yazılmasın diye
// her bölgeye bir şerit numarası (0..n). Açgözlü aralık bölme: başlangıca
// göre sıralı, bir bölge bitişi kendisinden önce olan ilk boş şeride girer.
// 164 canlı görüntüsü: günlerdir aktif 3 anomali tam pencere → üç 7 % dolgu
// üst üste (%20 turuncu zemin) + üç etiket aynı yerde. Şimdi: dolgu
// BİRLEŞİK aralıklarla tek kat (mergeIntervals), şerit + etiket şerit
// numarası kadar aşağıda kendi satırında. SAF, testli.
export function assignLanes(regions: { fromSec: number; toSec: number }[]): number[] {
  const order = regions.map((r, i) => i).sort((a, b) => regions[a].fromSec - regions[b].fromSec || regions[a].toSec - regions[b].toSec);
  const laneEnd: number[] = [];
  const lanes = new Array<number>(regions.length).fill(0);
  for (const i of order) {
    const r = regions[i];
    let lane = laneEnd.findIndex(end => end <= r.fromSec);
    if (lane < 0) { lane = laneEnd.length; laneEnd.push(r.toSec); } else laneEnd[lane] = r.toSec;
    lanes[i] = lane;
  }
  return lanes;
}

/** Çakışan/bitişik aralıkların birleşimi (dolgu tek kat çizilsin diye); ilk bölgenin rengi. */
export function mergeIntervals(regions: { fromSec: number; toSec: number; color?: string }[]): { fromSec: number; toSec: number; color?: string }[] {
  const s = regions.slice().sort((a, b) => a.fromSec - b.fromSec);
  const out: { fromSec: number; toSec: number; color?: string }[] = [];
  for (const r of s) {
    const last = out[out.length - 1];
    if (last && r.fromSec <= last.toSec) { if (r.toSec > last.toSec) last.toSec = r.toSec; }
    else out.push({ fromSec: r.fromSec, toSec: r.toSec, color: r.color });
  }
  return out;
}

export const LANE_H = 14; // şerit satırı (css px): 3px şerit + 10px etiket, taban çizgisi LANE_H-1 — isabet satırıyla AYNI sabit (v0.10.182 #10)

// v0.10.169 (operatör: "neden bantlar var" → "1 kalsın"): pencerenin en az
// bu kadarını kaplayan bölge KRONİKTİR (günlerdir aktif anomali) — dolgusu
// bilgi taşımaz, zemini boyar. Dolgu çizilmez; şerit + etiket («· pencere
// boyu» ekiyle) kalır. Kısa/gerçek anomali dolgusunu korur.
export const CHRONIC_COVERAGE = 0.9;
export function isChronic(from: number, to: number, xMin: number, xMax: number): boolean {
  const span = xMax - xMin;
  return span > 0 && (to - from) / span >= CHRONIC_COVERAGE;
}

// ── v0.10.180 — bant isabeti (etüt «anomali işaretleri» dilim 2: hit-test + tooltip) ──
//
// İsabet yalnız ŞERİT SATIRINDA (lane × LANE_H, çizim alanının üstü): seri
// hover'ı çalınmaz, kronik/kısa ayrımı yok — çizilen her bölgenin şeridi var.
// Koordinatlar CSS px, u.over'a göre (u.cursor.left/top ile aynı uzay).

export interface RegionLaneItem { x1: number; x2: number; lane: number }

/** SAF: (px,py) hangi bölgenin şerit satırına düşüyor; -1 = hiçbiri. Çakışmada SON çizilen üstte → son eş. */
export function regionLaneHit(items: RegionLaneItem[], px: number, py: number, laneH = LANE_H): number {
  let hit = -1;
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    const top = it.lane * laneH;
    if (py >= top && py < top + laneH && px >= it.x1 && px <= it.x2) hit = i;
  }
  return hit;
}

/** Canlı x ölçeğinde bölgelerin şerit geometrisi (CSS px); pencere dışı bölge atlanır (index korunur: x1=x2=NaN). */
export function regionLaneItems(u: uPlot, regions: ChartTimeRegion[], xUnit = 1): RegionLaneItem[] {
  const xMin = u.scales.x.min ?? 0;
  const xMax = u.scales.x.max ?? 0;
  const scaled = regionsToScale(regions, xUnit);
  const lanes = assignLanes(scaled);
  return scaled.map((rg, i) => {
    const cl = clampRegion(rg.fromSec, rg.toSec, xMin, xMax);
    if (!cl) return { x1: NaN, x2: NaN, lane: lanes[i] };
    const x1 = u.valToPos(cl.from, 'x', false);
    const x2 = Math.max(x1 + 1, u.valToPos(cl.to, 'x', false));
    return { x1, x2, lane: lanes[i] };
  });
}

/** İmleç (CSS px, u.over) bir bölgenin şeridinde mi → bölge; değilse null. */
export function regionAt(u: uPlot, regions: ChartTimeRegion[] | undefined, xUnit: number, cssX: number, cssY: number): ChartTimeRegion | null {
  if (!regions?.length) return null;
  const i = regionLaneHit(regionLaneItems(u, regions, xUnit), cssX, cssY);
  return i >= 0 ? regions[i] : null;
}

/** Bölge süresi, kısa Türkçe: 45 s · 12 dk · 3.5 sa · 2.1 g */
export function fmtRegionSpan(sec: number): string {
  if (!(sec > 0)) return '—';
  if (sec < 90) return `${Math.round(sec)} s`;
  if (sec < 5400) return `${Math.round(sec / 60)} dk`;
  if (sec < 172800) return `${(sec / 3600).toFixed(1)} sa`;
  return `${(sec / 86400).toFixed(1)} g`;
}

export function drawTimeRegions(u: uPlot, regions: ChartTimeRegion[], xUnit = 1): void {
  if (regions.length === 0) return;
  const xMin = u.scales.x.min ?? 0;
  const xMax = u.scales.x.max ?? 0;
  regions = regionsToScale(regions, xUnit);
  const lanes = assignLanes(regions);
  const dpr = (typeof devicePixelRatio !== 'undefined' ? devicePixelRatio : 1) || 1;
  const ctx = u.ctx;
  ctx.save();
  ctx.font = `${10 * dpr}px ui-monospace, monospace`;
  // v0.10.168 — hiza AÇIKÇA sol/üst: uPlot'un y-ekseni tik etiketleri
  // textAlign='right' bırakıyor ve save() o sızıntıyı yakalıyor; etiket
  // x1'den SOLA uzayıp eksen etiketlerini eziyordu (166 canlı görüntüsü:
  // «op ×175.0» kırpık). drawTimeRegions.test.ts sahte canvas'la pinler.
  ctx.textAlign = 'left';
  ctx.textBaseline = 'alphabetic';
  // Arka-plan gölgesi: birleşik aralıklar, TEK kat (çakışma koyulaştırmaz);
  // kronik (pencere boyu) bölgeler dolguya GİRMEZ (v0.10.169).
  const clamped = regions.map(r => clampRegion(r.fromSec, r.toSec, xMin, xMax));
  const chronic = clamped.map(cl => !!cl && isChronic(cl.from, cl.to, xMin, xMax));
  for (const mg of mergeIntervals(regions.filter((_, i) => clamped[i] && !chronic[i]))) {
    const cl = clampRegion(mg.fromSec, mg.toSec, xMin, xMax);
    if (!cl) continue;
    const x1 = u.valToPos(cl.from, 'x', true);
    const x2 = u.valToPos(cl.to, 'x', true);
    ctx.globalAlpha = REGION_FILL_ALPHA;
    ctx.fillStyle = resolveVar(mg.color ?? 'var(--err)');
    ctx.fillRect(x1, u.bbox.top, Math.max(1, x2 - x1), u.bbox.height);
  }
  ctx.globalAlpha = 1;
  for (let ri = 0; ri < regions.length; ri++) {
    const rg = regions[ri];
    const cl = clamped[ri];
    if (!cl) continue;
    const colour = resolveVar(rg.color ?? 'var(--err)');
    const x1 = u.valToPos(cl.from, 'x', true);
    const x2 = u.valToPos(cl.to, 'x', true);
    const w = Math.max(1, x2 - x1);
    const laneTop = u.bbox.top + lanes[ri] * LANE_H * dpr;
    if (laneTop >= u.bbox.top + u.bbox.height) continue; // çizim alanını aşan şerit çizilmez
    // Şerit — bölgenin "başlık çubuğu", kendi şerit satırında.
    ctx.globalAlpha = REGION_STRIP_ALPHA;
    ctx.fillStyle = colour;
    ctx.fillRect(x1, laneTop, w, REGION_STRIP_H * dpr);
    ctx.globalAlpha = 1;
    // Küçük etiket — şeridin altında; sığmazsa kısalt, hiç sığmazsa çizme (fitLabel).
    if (rg.label) {
      const text = fitLabel('▮ ' + rg.label + (chronic[ri] ? ' · pencere boyu' : ''), w - 8 * dpr, s => ctx.measureText(s).width);
      if (text) {
        ctx.fillStyle = colour;
        ctx.fillText(text, x1 + 4 * dpr, laneTop + (LANE_H - 1) * dpr);
      }
    }
  }
  ctx.restore();
}

// ── Exemplar ◆ katmanı (v0.9.744, Explore v2) ───────────────────────────
//
// TimeSeriesPanel'in Phase 3.2 çiziminin CorePanel karşılığı: her
// (görünür seri, trace'li bucket) için elmas; panel-arka-planı halosu
// aynı renkli çizgi üstünde okunurluk sağlar. Renkler ÇİZİM anında
// çözülür (tema-canlı). CorePanel'in x ölçeği MS (Grafana frame
// sözleşmesi) — TimeSeriesPanel'deki /1e9 burada /1e6.
export interface ChartExemplar {
  time: number;   // unix NANOS (bucket başlangıcı)
  value: number;  // elmasın y konumu (seri değeri)
  traceId: string;
  kind: 'slow' | 'error' | 'otlp';
}

export function exemplarColorVar(kind: ChartExemplar['kind']): string {
  return kind === 'error' ? 'var(--err)' : kind === 'otlp' ? 'var(--purple)' : 'var(--accent2)';
}

export function drawExemplars(
  u: uPlot,
  perSeries: (ChartExemplar[] | undefined)[],
  visible: boolean[],
  resolve: (cssVar: string) => string,
  halo: string,
): void {
  const ctx = u.ctx;
  const minX = u.scales.x.min ?? 0;
  const maxX = u.scales.x.max ?? 0;
  ctx.save();
  for (let si = 0; si < perSeries.length; si++) {
    const exs = perSeries[si];
    if (!exs?.length || visible[si] === false) continue;
    for (const ex of exs) {
      const t = ex.time / 1e6; // ns → ms
      if (t < minX || t > maxX) continue;
      const x = u.valToPos(t, 'x', true);
      const y = u.valToPos(ex.value, 'y', true);
      ctx.beginPath();
      ctx.moveTo(x, y - 4);
      ctx.lineTo(x + 4, y);
      ctx.lineTo(x, y + 4);
      ctx.lineTo(x - 4, y);
      ctx.closePath();
      ctx.fillStyle = resolve(exemplarColorVar(ex.kind)) || '#a371f7';
      ctx.strokeStyle = halo;
      ctx.lineWidth = 1;
      ctx.fill();
      ctx.stroke();
    }
  }
  ctx.restore();
}

// exemplarAt — tık isabeti: CSS-piksel konumuna en yakın ◆ (tolerans
// içinde) ya da null. Tık trace açar; panel tık-eylemi (navigasyon)
// İKİNCİ önceliktir — çağıran önce bunu sorar.
export function exemplarAt(
  u: uPlot,
  perSeries: (ChartExemplar[] | undefined)[],
  visible: boolean[],
  cssX: number,
  cssY: number,
  tolPx = 6,
): ChartExemplar | null {
  let best: ChartExemplar | null = null;
  let bestD = tolPx;
  for (let si = 0; si < perSeries.length; si++) {
    const exs = perSeries[si];
    if (!exs?.length || visible[si] === false) continue;
    for (const ex of exs) {
      const x = u.valToPos(ex.time / 1e6, 'x', false);
      const y = u.valToPos(ex.value, 'y', false);
      const d = Math.max(Math.abs(x - cssX), Math.abs(y - cssY));
      if (d <= bestD) { bestD = d; best = ex; }
    }
  }
  return best;
}
