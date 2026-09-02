// lib/chartStep.ts — GRAN-A (v0.8.245): Grafana-style width-aware step.
//
// Until now the frontend sent step=0 (auto) and the backend picked a bucket
// off its ~120-point ladder (1h → 30s), so a 4K monitor and a laptop half-pane
// got the same coarse resolution. This module lets each chart ASK for a step
// matched to the pixels it actually has (~2px per point, Grafana's rule of
// thumb). The frontend may request aggressively fine steps: the backend's
// min-step clamp (v0.8.243) guarantees the effective step never undercuts the
// metric's export interval, so a 1s request against a 15s-export metric
// degrades safely server-side.
//
// PURE — no DOM, no Date. The DOM half lives in lib/useContentWidth.ts.

// Grafana-ish step ladder (seconds): 1s…2h plus 4h/12h/1d for wide windows.
// Steps snap UP to a rung so buckets stay human-readable (5s, not 5.14s).
export const STEP_RUNGS = [
  1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800,
  3600, 7200, 14400, 43200, 86400,
];

const LAST_RUNG = STEP_RUNGS[STEP_RUNGS.length - 1]; // 86400 (1d)

// stepForWidth — the step (seconds) a chart of `widthPx` should request for a
// `rangeSec` window. targetPoints ≈ one point per 2px, clamped to [120, 720]:
// never coarser than the backend's old ~120-point auto ladder, never more
// points than a wide monitor can distinguish. The raw step rounds UP to the
// first rung that covers it (rung >= raw — a smaller rung would overshoot the
// point budget); beyond the 1d rung it rounds up to a whole multiple of 1d.
export function stepForWidth(rangeSec: number, widthPx: number): number {
  return stepForPoints(rangeSec, Math.min(720, Math.max(120, Math.floor(widthPx / 2))));
}

// stepForPoints — v0.9.484 (operatör onayı: Response time "root spanler için
// multichart"). stepForWidth'in nokta→rung yarısı, AYRI export.
//
// Neden: /api/spans/metric `step` (saniye) alır, /api/spans/metric-batch
// `maxDataPoints` alır. Overview'un toplam görünümü batch'i redMdp ile
// çağırıyor; operasyon kırılımı AYNI pencereyi aynı çözünürlükte görmeli,
// yoksa iki görünüm arasında geçiş yapan operatör bucket boyu değiştiği
// için zıplayan bir grafik görür. Nokta bütçesini rung'a çevirmenin tek
// yeri burası olsun diye stepForWidth de buradan geçiyor — davranışı
// birebir aynı (clamp'ı çağıran tarafta kaldı).
//
// Rung'a snap ETMEK aynı zamanda cache-key disiplini (v0.8.270): step
// sunucu cache anahtarına biniyor, sınırlı kardinalite şart.
export function stepForPoints(rangeSec: number, targetPoints: number): number {
  if (!Number.isFinite(rangeSec) || rangeSec <= 0) return STEP_RUNGS[0];
  const pts = Number.isFinite(targetPoints) ? Math.max(1, Math.floor(targetPoints)) : 1;
  const raw = rangeSec / pts;
  for (const rung of STEP_RUNGS) {
    if (rung >= raw) return rung;
  }
  return Math.ceil(raw / LAST_RUNG) * LAST_RUNG;
}

// quantizeWidth — round a live pixel width into 200px buckets, clamped to
// [400, 2400]. The bucket (not the raw width) feeds stepForWidth, so a drag-
// resize only changes the requested step — and thus the react-query cache key
// — when the width crosses a bucket boundary, instead of refetching per
// ResizeObserver tick.
export function quantizeWidth(px: number): number {
  if (!Number.isFinite(px)) return 1200; // same fallback as useContentWidth
  return Math.min(2400, Math.max(400, Math.round(px / 200) * 200));
}

// panelMaxDataPoints — v0.9.391 (grafik-audit Faz B): batch RED
// sorgularının nokta bütçesi. ÖLÇÜLMÜŞ ref genişliği DEĞİL, viewport
// türevi kullanır — bilinçli: Service.tsx bundle-öncesi PREFETCH ile
// Overview'un useQuery'si AYNI queryKey'i üretmek zorunda; ref ancak
// mount'ta ölçülür, prefetch anında yoktur. Formül iki tarafta da
// deterministik: (viewport - sidebar) / kolon → quantizeWidth (200px
// bucket — resize tick'i başına refetch yok) → px/2 nokta.
export function panelMaxDataPoints(cols: number): number {
  const vw = typeof window !== 'undefined' ? window.innerWidth : 1440;
  const content = Math.max(400, vw - 260); // sidebar payı
  return Math.max(120, Math.min(2000, Math.round(quantizeWidth(content / Math.max(1, cols)) / 2)));
}

// ── Parite dilim 3 (v0.9.707): en kötü yüzeylerin bütçe yardımcıları ──

// barPanelMaxDataPoints — BAR yüzeylerinin bütçesi (v0.9.715,
// operatör-bildirimi: "Barlar çok küçülmüş").
//
// v0.9.707 en kötü yüzeylere nokta bütçesi verirken ÇİZGİ bütçesini
// (px/2) kullandı; 3 saatlik Traces şeridinde ~360 adet ~4px bar üretti.
// Bar çizgi değildir: okunabilir bar ~12px ister (Grafana bar/histogram
// yüzeyleri de kova sayısını böyle düşürür). px/12, taban 30 (eski sabit
// davranışın alt sınırı — daha azı bilgi kaybı), tavan 240 (ES/heatmap
// sunucu tavanıyla hizalı).
export function barPanelMaxDataPoints(cols = 1): number {
  const vw = typeof window !== 'undefined' ? window.innerWidth : 1440;
  const content = Math.max(400, vw - 260);
  return Math.max(30, Math.min(240, Math.round(quantizeWidth(content / Math.max(1, cols)) / 12)));
}

// logsBucketSec — Logs hacim şeridi + LogsHistogram İKİSİ İÇİN TEK kaynak.
// İkisi ayrı ama "aynı kalmalı" yorumlu iki kopya merdivendi (5s/30s/1m/
// 5m/15m) — 1 saatte 120 kova ≈ 0.1 nokta/px. Şimdi piksel bütçesinden:
// nokta ≤ min(240, panel bütçesi) — 240 tavanı ES maliyeti (date_histogram
// kova sayısı; v0.8.270 disiplini: rung-kuantalı → sınırlı kardinalite),
// 5 sn tabanı ES'in makul en ince kovası.
export function logsBucketSec(spanSec: number): number {
  // v0.9.715 — bar bütçesine geçti (bunlar da bar yüzeyi; Traces
  // şeridiyle aynı "küçülmüş bar" sınıfı).
  return Math.max(5, stepForPoints(spanSec, barPanelMaxDataPoints(1)));
}

// heatmapBucketCount — sabit 60/80 kova yerine genişlik-türevi sütun
// sayısı. ~12px/sütun Grafana heatmap yoğunluğu; sunucu tavanı 240
// (chstore/heatmap.go), tabanı 40 (daha azı lekeli görünüyor).
export function heatmapBucketCount(cols = 1): number {
  const vw = typeof window !== 'undefined' ? window.innerWidth : 1440;
  const content = Math.max(400, vw - 260);
  const px = quantizeWidth(content / Math.max(1, cols));
  return Math.max(40, Math.min(240, Math.round(px / 12)));
}

// ── v0.10.287 (chart audit D2 / Dilim 1.6): Thanos trend uçlarının bütçesi ──
// /api/clusters/deploy-trend + namespaces/pods-trend ?maxDataPoints= —
// sunucu (internal/thanos TrendMaxDataPointsRungs) bu basamaklara snap eder;
// iki liste Go testiyle çivili (TestThanosMDPRungsMatchFrontend). 480 =
// merdivenin bugünkü tavanı (bütçesiz istemci aynı seriyi görür).
export const THANOS_MDP_RUNGS = [120, 240, 480];

// thanosMaxDataPoints — panelMaxDataPoints(cols) basamağa YUKARI yuvarlanır
// (istemci en az istediği kadar nokta alır); tavan 480.
export function thanosMaxDataPoints(cols: number): number {
  const want = panelMaxDataPoints(cols);
  for (const r of THANOS_MDP_RUNGS) if (want <= r) return r;
  return THANOS_MDP_RUNGS[THANOS_MDP_RUNGS.length - 1];
}
