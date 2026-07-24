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
export function drawTimeRegions(u: uPlot, regions: ChartTimeRegion[]): void {
  if (regions.length === 0) return;
  const xMin = u.scales.x.min ?? 0;
  const xMax = u.scales.x.max ?? 0;
  const dpr = (typeof devicePixelRatio !== 'undefined' ? devicePixelRatio : 1) || 1;
  const ctx = u.ctx;
  ctx.save();
  ctx.font = `${10 * dpr}px ui-monospace, monospace`;
  for (const rg of regions) {
    const cl = clampRegion(rg.fromSec, rg.toSec, xMin, xMax);
    if (!cl) continue;
    const colour = resolveVar(rg.color ?? 'var(--err)');
    const x1 = u.valToPos(cl.from, 'x', true);
    const x2 = u.valToPos(cl.to, 'x', true);
    const w = Math.max(1, x2 - x1);
    // Arka-plan gölgesi.
    ctx.globalAlpha = REGION_FILL_ALPHA;
    ctx.fillStyle = colour;
    ctx.fillRect(x1, u.bbox.top, w, u.bbox.height);
    // Üst şerit — bölgenin "başlık çubuğu".
    ctx.globalAlpha = REGION_STRIP_ALPHA;
    ctx.fillRect(x1, u.bbox.top, w, REGION_STRIP_H * dpr);
    ctx.globalAlpha = 1;
    // Küçük etiket — sığmazsa kısalt, hiç sığmazsa çizme (fitLabel).
    if (rg.label) {
      const text = fitLabel('▮ ' + rg.label, w - 8 * dpr, s => ctx.measureText(s).width);
      if (text) {
        ctx.fillStyle = colour;
        ctx.fillText(text, x1 + 4 * dpr, u.bbox.top + (REGION_STRIP_H + 10) * dpr);
      }
    }
  }
  ctx.restore();
}
