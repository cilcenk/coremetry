// seriesFocus (v0.9.793) — "bir seriye odaklan" görsel sözleşmesinin SAF
// çekirdeği. TimeSeriesPanel'in focusedLabel davranışının (TSP:446 +
// TSP:904-915) CorePanel'e taşınan hâli.
//
// TSP uPlot'un KENDİ mekanizmasını kullanıyor: `focus: {alpha: 0.35}` üst
// düzey seçeneği + `u.setSeries(i, {focus: true})`. Bu yol CorePanel'de
// KAPALI: panel config'i @grafana/ui'nin UPlotConfigBuilder'ından çıkıyor ve
// builder üst düzey `focus`u DEFAULT_PLOT_CONFIG'de {alpha: 1} olarak
// sabitliyor — ne bir setter'ı var (setCursor/addSeries/... listesinde yok)
// ne de emitteği PlotConfig tipi `focus` anahtarını taşıyor
// (Pick<Options, 'mode'|'series'|'scales'|'axes'|'cursor'|'bands'|'hooks'|
// 'select'|'tzDate'|'padding'>). uPlot içinde `_setAlpha = focus.alpha != 1`
// olduğu için alpha 1 kaldığı sürece setSeries(focus) HİÇBİR şey yapmaz.
//
// O yüzden alpha'yı SERİ BAŞINA yazıyoruz: uPlot'un çizim döngüsü her seri
// için `if (ctxAlpha != s.alpha) ctx.globalAlpha = s.alpha` yapar ve seri
// bitince ctxAlpha'yı GİRİŞ değerine geri alır — yani overlay'lerimize
// (eşik/bölge/exemplar) alpha sızmaz. `Series.alpha` uPlot'un PUBLIC tipinde
// (uPlot.d.ts:946); `as any` yok, kapatılmış API kullanımı yok.
//
// Bu modül saf: hangi serinin hangi alpha/genişlikle çizileceğini hesaplar,
// uygulamayı çağırana bırakır (CorePanel: mutasyon + redraw, rebuild YOK —
// v0.9.704 destroy/recreate dersi).

// Odaklanmamış serilerin opaklığı. TSP 0.35 kullanıyor; CorePanel'in
// varsayılan dolgusu (fillOpacity 12 + Opacity gradyanı) TSP'ninkinden
// solgun olduğu için ayrım 0.25'te okunur kalıyor — spec "diğerleri ~0.25".
export const FOCUS_DIM_ALPHA = 0.25;
// Odaklanan serinin çizgisi yarım piksel kalınlaşır: renk aynı kalırken
// "seçili" hissi verir (Grafana'nın lejant hover vurgusu).
export const FOCUS_WIDTH_BOOST = 0.5;

export interface FocusStyle {
  alpha: number;
  width: number;
}

// resolveFocusIdx — etiketten 0-tabanlı VERİ serisi index'i (uPlot'un
// 1-tabanlı series[] dizisi DEĞİL; çevirmek çağıranın işi). Odak yoksa ya da
// etiket bu paneldeki seri kümesinde yoksa -1: "odak yok" ile "bilinmeyen
// etiket" AYNI sonuca çıkar, çünkü ikisinde de soluklaştırılacak bir şey yok
// (Explore'da GroupTable başka bir panelin serisinde geziniyor olabilir —
// o panel toptan solmamalı).
export function resolveFocusIdx(names: readonly string[], focused: string | null | undefined): number {
  if (focused == null) return -1;
  return names.indexOf(focused);
}

// focusSeriesStyle — her VERİ serisi için {alpha, width}.
//   focusIdx < 0  → odak yok: hepsi tam alpha, taban genişlik (nötr durum,
//                   yani unfocus da bu fonksiyondan çıkar — ikinci bir
//                   "geri al" kod yolu yok).
//   focusIdx >= 0 → odaklanan tam alpha + taban+0.5, diğerleri DIM.
// baseWidths eksikse (kısa dizi) 1.5 varsayılır — CorePanel'in line tabanı.
export function focusSeriesStyle(
  baseWidths: readonly (number | undefined)[],
  focusIdx: number,
): FocusStyle[] {
  return baseWidths.map((w, i) => {
    const base = typeof w === 'number' && isFinite(w) ? w : 1.5;
    if (focusIdx < 0) return { alpha: 1, width: base };
    return i === focusIdx
      ? { alpha: 1, width: base + FOCUS_WIDTH_BOOST }
      : { alpha: FOCUS_DIM_ALPHA, width: base };
  });
}
