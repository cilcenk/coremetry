// dashboard/heatmapPivot — pano heatmap panelinin jest kapısı (v0.9.947, D4/Ö24).
//
// Ö24: "Dashboard heatmap paneli salt-okunur: hücre-tık, kutu seçim, brush
// yok." Aynı görselleştirme Service ve Explore'da hem örnek-trace tıkı hem
// kutu seçimi taşıyor; panoda hiçbir jest yoktu.
//
// AMA jestleri KOŞULSUZ bağlamak yeni bir yalan üretirdi ve bu modül tam
// olarak onun için var:
//
// Service/Explore heatmap'i SPAN SÜRESİ çiziyor — y ekseni gerçekten
// gecikme, dolayısıyla "bu bandın trace'lerini göster" doğru bir soru.
// Pano paneli ise bir METRİK HİSTOGRAMI çiziyor (histogramHeatmap.ts kendi
// yorumunda söylüyor: "metric-histogram datapoints, not spans",
// countNoun:'samples'). `jvm_memory_bytes` histogramında bir hücreye
// tıklayıp "süresi 2–4 ms arasındaki trace'ler" listelemek, kullanıcıya
// var olmayan bir ilişki göstermek olurdu — boş modal değil, YANLIŞ modal.
//
// Kapı bu yüzden BİRİMDE: yalnız süre birimli histogramlar (ms / s) trace
// pivotu taşır. Ötekiler jestsiz kalır — eksik bir özellik, uydurulmuş bir
// bağlantıdan iyidir.
//
// SAF — tablo testleri heatmapPivot.test.ts.

// Süre birimleri: histogramHeatmap.ts'in ms'e çevirdiği ('s'/'seconds')
// yazımlar + zaten ms olanlar. Liste ORAYLA aynı hizada tutulmalı: oradaki
// `toMs` çarpanı hangi yazımları tanıyorsa, pivot da onları tanımalı.
const DURATION_UNITS = new Set(['s', 'seconds', 'ms', 'milliseconds']);

/**
 * heatmapPivotable — bu panelin y ekseni bir SÜRE mi?
 *
 * Boş/bilinmeyen birim FALSE: birimini söylemeyen bir histogramı süre
 * saymak, tam da kaçındığımız varsayım olurdu.
 */
export function heatmapPivotable(unit: string | undefined): boolean {
  return DURATION_UNITS.has((unit ?? '').trim().toLowerCase());
}

/**
 * heatmapTracesHref — kutu seçiminden /traces pivotu.
 *
 * Pencere `custom:` ms aralığı olarak yazılıyor (sayfa aralığı DEĞİL):
 * operatörün sürüklediği kutu SORUNUN KENDİSİ, onu sayfanın geniş
 * penceresine yaymak seçimi anlamsızlaştırırdı.
 *
 * service boşsa servis çipi HİÇ yazılmaz — "tüm servisler" bir değer
 * değil, yokluk.
 */
export function heatmapTracesHref(box: {
  timeFromNs: number; timeToNs: number; lowDurMs: number; highDurMs: number;
}, service?: string): string {
  const lo = Math.max(0, Math.floor(box.lowDurMs));
  const hi = Math.ceil(box.highDurMs);
  const fromMs = Math.floor(box.timeFromNs / 1e6);
  const toMs = Math.ceil(box.timeToNs / 1e6);
  return '/traces?'
    + (service ? `service=${encodeURIComponent(service)}&` : '')
    + `minMs=${lo}&maxMs=${hi}`
    + `&range=custom:${fromMs}-${toMs}`
    + '&view=list&rootOnly=false';
}
