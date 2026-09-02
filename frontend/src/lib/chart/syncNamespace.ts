// syncNamespace.ts — v0.10.289 (chart audit Dilim 1.3): uPlot.sync grup
// adının MOTOR AD ALANI tek yerden üretilir.
//
// '-ms' eki süs değil: uPlot.sync imleci karşı grafiğin ÖLÇEĞİNE VALUE
// olarak taşır. CorePanel ms-eksenli, hat B (TimeChart / OverviewChart /
// ChartCard) saniye-eksenli; aynı anahtarı paylaşan iki motor crosshair'i
// 1000× yanlış yere koyar (v0.9.789, Pod sayfası `podjmx:` kökü). Ek beş
// yerde elle yazılıyordu (MultiLineChart, Pod, DetailDrawer,
// databases/detailSections, endpoints/MetricTile) — sessiz kayıp riski:
// biri eki unutursa hata yok, uyarı yok, imleç sadece geçmez ya da 1000×
// kayar. Audit 1.3 "-ms kalksın" demişti; hat B emekli olana dek (Dilim
// 2.5) ek ŞART — kaldırılmadı, TEKLEŞTİRİLDİ. Hat B gidince bu dosya tek
// satırla `base` döner ve 46 site birlikte döner.
//
// Kural (syncNamespace.test.ts pinler): `-ms` literal'i src/ içinde YALNIZ
// burada yazılır.

export const CHART_SYNC_NAMESPACE = '-ms';

// msSyncKey — taban grup adı → CorePanel (ms-eksenli motor) grup adı.
// Boş taban → undefined (senkron yok).
export function msSyncKey(base: string | undefined | null): string | undefined {
  const b = (base ?? '').trim();
  return b ? b + CHART_SYNC_NAMESPACE : undefined;
}
