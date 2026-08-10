import { useSyncExternalStore } from 'react';

// cursorBus (explore-v2 Phase 3) — the shared crosshair time channel.
//
// Every QueryPanel publishes its uPlot cursor position here; ONLY the
// GroupTable (and anything else that opts in via useCursorTime) re-renders
// on cursor movement. The panels themselves never read the bus — uPlot's
// own cursor.sync mirrors the crosshair across charts — so the 60fps
// mousemove path stays out of React entirely (plan perf guard).
//
// v0.9.948 (UX denetimi D5 / Ö26) — pages/explore/ altından lib/chart/'a
// TAŞINDI. Kanalın Explore'a özgü hiçbir yanı yoktu ve ikinci tüketici
// Explore dışında doğdu: LatencyHeatmap. Heatmap bir CANVAS, yani
// uPlot.sync'e KATILAMAZ — uPlot yalnız kendi örneklerini senkronlar.
// Bileşenin kendi yorumu ("Same time axis as the metric line chart")
// paylaşılan bir zaman ekseni VAAT EDİYORDU ama imleç izi hiç çıkmıyordu;
// bu otobüs o vaadin tek uygulanabilir taşıyıcısı.
//
// TEK KANAL (grup anahtarı YOK) bilinçli: bir sayfada tek zaman ekseni
// var. Grup anahtarı eklemek, uPlot.sync'in `-ms` ad alanı sorununu
// (v0.9.945) ikinci bir yerde yeniden üretmek olurdu — bu otobüs
// SANİYE taşıyor, ölçek belirsizliği yok.
//
// Publishes are rAF-throttled: writes between frames collapse to the last
// value, and subscribers are only notified when the flushed value actually
// changed. uPlot sync means N panels publish the SAME time per frame — the
// change check dedupes those to zero extra notifications.

type Listener = () => void;

let current: number | null = null;   // cursor time, unix SECONDS; null = no cursor
let pending: number | null = null;
let scheduled = false;
const listeners = new Set<Listener>();

function flush() {
  scheduled = false;
  if (pending === current) return;
  current = pending;
  listeners.forEach(l => l());
}

// publishCursor — called from TimeSeriesPanel's setCursor hook (through a
// ref, so it never forces a chart rebuild). Safe to call at mousemove rate.
export function publishCursor(timeSec: number | null): void {
  pending = timeSec;
  if (scheduled) return;
  scheduled = true;
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(flush);
  } else {
    // Non-browser (vitest) fallback — flush on a microtask.
    queueMicrotask(flush);
  }
}

// subscribe/getCursor — the store half of useSyncExternalStore, exported so
// the vitest suite can pin the throttle semantics without a DOM renderer.
export function subscribe(l: Listener): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function getCursor(): number | null {
  return current;
}

// useCursorTime — the ONLY React entry point. Components using this hook
// re-render once per animation frame at most while the cursor moves.
export function useCursorTime(): number | null {
  return useSyncExternalStore(subscribe, getCursor, getCursor);
}

// valueAtCursor — a time/value series' value at the crosshair. cursorSec is
// unix SECONDS (what the bus carries); points carry unix NANOS (the TSSeries
// shape). Returns the NEAREST sample's value so the GroupTable's @cursor column
// matches what the operator sees under the crosshair (uPlot snaps to the
// nearest x). NaN when the series is empty or the nearest sample is a gap.
// Points are assumed time-ascending (the API series shape).
export function valueAtCursor(
  points: { time: number; value: number | null }[],
  cursorSec: number,
): number {
  if (points.length === 0) return NaN;
  const target = cursorSec * 1e9;
  let lo = 0, hi = points.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (points[mid].time < target) lo = mid + 1;
    else hi = mid;
  }
  // lo = first sample with time >= target; the nearer of it and its predecessor.
  let best = lo;
  if (lo > 0 && Math.abs(points[lo - 1].time - target) <= Math.abs(points[lo].time - target)) {
    best = lo - 1;
  }
  const v = points[best].value;
  return v == null || !isFinite(v) ? NaN : v;
}

// Test hook — reset module state between cases.
export function __resetCursorBus(): void {
  current = null;
  pending = null;
  scheduled = false;
  listeners.clear();
}
