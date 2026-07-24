import type uPlot from 'uplot';

// cursorOpts (Grafana-parite M1) — dört preset'in (OverviewChart / TimeChart /
// MultiLineChart / TimeSeriesPanel) KOPYA cursor.drag + hooks.setSelect
// bloklarının tek kaynağı. SIFIR davranış değişikliği hedefi: her preset kendi
// bayraklarını (dragX / setScale / minWidthPx) ve kendi onZoom callback'ini
// geçirir — TC ms'e çevirir, MLC/OVC ref'ten okur, TSP doğrudan closure.
// setSelect hook'u yalnız onZoom VARSA döner; preset build-sig'lerindeki
// hasZoom PRESENCE sözleşmesi (none→some = rebuild) aynen geçerli kalır.

export interface SelectLike { left: number; width: number }

// selectRangeSec — saf çekirdek: uPlot select kutusundan x aralığı çıkarır
// (posToVal'ın birimi neyse o — pratikte unix saniye). Kazara mini
// sürüklemeler (width < minWidthPx) ve sonlu olmayan dönüşümler null döner;
// sonuç her zaman from <= to sıralı.
export function selectRangeSec(
  sel: SelectLike | null | undefined,
  posToVal: (px: number) => number,
  minWidthPx = 4,
): { from: number; to: number } | null {
  if (!sel || sel.width < minWidthPx) return null;
  const a = posToVal(sel.left);
  const b = posToVal(sel.left + sel.width);
  if (!isFinite(a) || !isFinite(b)) return null;
  return { from: Math.min(a, b), to: Math.max(a, b) };
}

export interface CursorZoomSpec {
  // uPlot.sync anahtarı — aynı key'li kardeş grafiklerle imleç senkronu.
  syncKey?: string;
  // Drag bırakınca [from,to] (unix sec). YOKSA setSelect hook'u üretilmez
  // ve preset drag.setScale'i kendi bayrağıyla belirler.
  onZoom?: (fromSec: number, toSec: number) => void;
  // cursor.drag.x — default true (TC: !!onBrush).
  dragX?: boolean;
  // cursor.drag.setScale — default true (OVC/TSP true, MLC !!onZoom, TC false).
  setScale?: boolean;
  // Kazara-sürükleme eşiği px — default 4 (TC 2).
  minWidthPx?: number;
}

// buildCursorOpts — preset'in cursor'ına spread'lenecek drag(+sync) parçası ve
// hooks.setSelect dizisi (onZoom yoksa undefined; engine undefined-değerli
// hook key'lerini zaten ayıklıyor, uPlot fire() patlamaz).
export function buildCursorOpts(spec: CursorZoomSpec): {
  cursor: { drag: { x: boolean; y: false; setScale: boolean }; sync?: { key: string } };
  setSelect: ((u: uPlot) => void)[] | undefined;
} {
  const { syncKey, onZoom, dragX = true, setScale = true, minWidthPx = 4 } = spec;
  return {
    cursor: {
      drag: { x: dragX, y: false, setScale },
      ...(syncKey ? { sync: { key: syncKey } } : {}),
    },
    setSelect: onZoom
      ? [
          (u: uPlot) => {
            const r = selectRangeSec(u.select, px => u.posToVal(px, 'x'), minWidthPx);
            if (!r) return;
            onZoom(r.from, r.to);
            // Gri seçim bandını temizle — aksi halde parent yeni aralığı
            // devralana kadar ekranda kalır (dört preset'teki mevcut reset).
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ]
      : undefined,
  };
}
