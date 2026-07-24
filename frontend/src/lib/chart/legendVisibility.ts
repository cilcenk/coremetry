// legendVisibility.ts (Grafana-parite #2) — lejant tıkı → seri görünürlüğü
// saf çekirdeği. Dört preset'in lejant etkileşimi aynı kurallara delege eder
// ve jest dili TEK (review 8/8 #8 — StatsLegend'in ters eşlemesi düzeltildi):
//   düz tık = isolate (yalnız o seri), Ctrl/Cmd-tık = toggle (gizle/göster)
//   — StatsLegend (OVC/TC) da, TimeSeriesPanel lejantı + MultiLineChart
//   uPlot-lejantı da (yerleşik v0.5.364 sözleşmesi + Grafana kontratı).
// Jest→işlem eşlemesi preset'te; İŞLEM semantiği burada, testli
// (legendVisibility.test.ts). Uygulama tarafı hep uPlot
// setSeries(i+1, {show}) — rebuild yok, zoom/imleç yaşar; y ekseni uPlot'un
// görünür-seriden autoscale'i + zoomlu fast-path'te yRefitScale ile refit olur.
//
// Kural: bir işlem sonucu HİÇBİR seri görünür kalmayacaksa hepsi geri gelir
// ("hepsi gizliyse hepsini geri getir") — boş grafiğe kilitlenmek operatöre
// hiçbir şey söylemez (Grafana da isolate-toggle'da böyle döner).

// toggleSeriesVisibility — i'nin görünürlüğünü çevir; sonuç tamamen boşsa
// hepsini geri getir. Out-of-range i → değişmemiş kopya.
export function toggleSeriesVisibility(vis: readonly boolean[], i: number): boolean[] {
  if (i < 0 || i >= vis.length) return [...vis];
  const next = [...vis];
  next[i] = !next[i];
  if (next.every(v => !v)) return next.map(() => true);
  return next;
}

// isolateSeriesVisibility — yalnız i görünür; i ZATEN tek görünense hepsini
// geri getir (isolate-toggle). Out-of-range i → değişmemiş kopya.
export function isolateSeriesVisibility(vis: readonly boolean[], i: number): boolean[] {
  if (i < 0 || i >= vis.length) return [...vis];
  const onlyThisVisible = vis[i] && vis.every((v, k) => (k === i ? true : !v));
  if (onlyThisVisible) return vis.map(() => true);
  return vis.map((_, k) => k === i);
}

// resetSeriesVisibility — n serinin hepsi görünür (rebuild reset'i).
export function resetSeriesVisibility(n: number): boolean[] {
  return Array.from({ length: Math.max(0, n) }, () => true);
}

// ── Persist katmanı (Grafana-parite madde 4 — yüzey sweep'i) ────────────────
// Kullanıcının lejant seçimi chart-başına localStorage'da KALICI. Saklanan
// şey GİZLİ SERİ ETİKETLERİ (index değil): seri kümesi/sırası değişse de
// seçim taşınır; seride artık olmayan etiketler doğal düşer. Kayıt yokluğu
// (null) ile boş kayıt ([]) AYRI anlamlıdır: null = kullanıcı hiç seçmedi
// (default uygulanır), [] = kullanıcı bilinçli "hepsi görünür" dedi
// (default'u ezer — "kullanıcı seçimi her zaman default'u ezer" kuralı).
// Saf çekirdek (encode/decode/visibilityFor/defaultLatencyHidden) node'da
// testli; localStorage I/O'su ince, guard'lı sarmalayıcılarda.

export const LEGEND_VIS_KEY_PREFIX = 'cm.legendVis:';

// encodeHiddenLabels — görünürlük dizisi + etiketler → saklanacak ham JSON.
// Aynı etiketin kopyaları (MLC compare-ghost ikizleri) Set ile teklenir.
// v0.9.206 review-fix: bir etiket ancak onu taşıyan TÜM indexler gizliyse
// "gizli" yazılır. Eski HERHANGİ-index-gizli semantiği compare modunda
// isolate'i zehirliyordu: ghost ikiz ham etiketi paylaştığından izole edilen
// etiket bile (ikizi gizli diye) kayda giriyor, TÜM etiketler gizli görünüp
// visibilityFor'un restore-all kuralına takılıyor ve artık-null-olmayan kayıt
// p99 default'unu kalıcı eziyordu. Tekil-etiketli çağıranlar (OVC/TSP) için
// eski davranışın birebiri.
export function encodeHiddenLabels(labels: readonly string[], vis: readonly boolean[]): string {
  const hidden = new Set<string>();
  const visible = new Set<string>();
  for (let i = 0; i < labels.length; i++) {
    if (vis[i] === false) hidden.add(labels[i]); else visible.add(labels[i]);
  }
  for (const l of visible) hidden.delete(l);
  return JSON.stringify([...hidden]);
}

// decodeHiddenLabels — ham localStorage değeri → gizli etiket listesi.
// Bozuk/yabancı değer null (kayıt yok say — default kazanır).
export function decodeHiddenLabels(raw: string | null | undefined): string[] | null {
  if (raw == null) return null;
  try {
    const v = JSON.parse(raw);
    if (!Array.isArray(v) || !v.every(x => typeof x === 'string')) return null;
    return v;
  } catch { return null; }
}

// visibilityFor — etiketler + (kalıcı gizli seçim | null) + (default gizli |
// null) → görünürlük dizisi. Öncelik: kullanıcı seçimi > default > hepsi
// görünür. Sonuç tamamen gizli kalacaksa hepsi geri gelir (çekirdek kuralı —
// boş grafiğe kilitlenme yok).
export function visibilityFor(
  labels: readonly string[],
  storedHidden: readonly string[] | null,
  defaultHidden?: readonly string[] | null,
): boolean[] {
  const source = storedHidden ?? defaultHidden;
  if (!source || source.length === 0) return labels.map(() => true);
  const hidden = new Set(source);
  const vis = labels.map(l => !hidden.has(l));
  if (vis.length > 0 && vis.every(v => !v)) return vis.map(() => true);
  return vis;
}

// defaultLatencyHidden — operatör-onaylı latency default'u: avg + p50 + p95
// açık, p99 GİZLİ. İSTİSNA: threshold'u p99'a bağlı panel (keepP99) p99'u
// açık tutar. p99 yalnız YANINDA başka seri varken gizlenir (tek-serili p99
// grafiği boşalmasın); eşleşme case-insensitive ('P99' ↔ 'p99').
export function defaultLatencyHidden(
  labels: readonly string[],
  opts?: { keepP99?: boolean },
): string[] {
  if (opts?.keepP99) return [];
  const p99s = labels.filter(l => l.trim().toLowerCase() === 'p99');
  if (p99s.length === 0 || p99s.length === labels.length) return [];
  return [...new Set(p99s)];
}

// StorageLike — testlerde sahte storage geçirilebilsin diye minimal arayüz.
export interface StorageLike {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

function defaultStorage(): StorageLike | null {
  try {
    return typeof localStorage !== 'undefined' ? localStorage : null;
  } catch { return null; }
}

// loadLegendVisibility — chart anahtarının kalıcı gizli etiketleri; kayıt
// yok/bozuk/storage erişilemez → null (default uygulanır).
export function loadLegendVisibility(key: string, storage?: StorageLike | null): string[] | null {
  const s = storage !== undefined ? storage : defaultStorage();
  if (!s) return null;
  try { return decodeHiddenLabels(s.getItem(LEGEND_VIS_KEY_PREFIX + key)); } catch { return null; }
}

// saveLegendVisibility — kullanıcının lejant seçimini yaz. Hepsi-görünür de
// AÇIKÇA yazılır ([]) — "kullanıcı default'u geri açtı" bilgisi kaybolmasın.
export function saveLegendVisibility(
  key: string,
  labels: readonly string[],
  vis: readonly boolean[],
  storage?: StorageLike | null,
): void {
  const s = storage !== undefined ? storage : defaultStorage();
  if (!s) return;
  try { s.setItem(LEGEND_VIS_KEY_PREFIX + key, encodeHiddenLabels(labels, vis)); } catch { /* quota/priv — sessiz */ }
}
