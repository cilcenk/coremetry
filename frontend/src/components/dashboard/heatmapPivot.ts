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

import { tracesPivotHref } from '@/lib/pivotHref';

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
 * Pencere KUTUDAN geliyor (sayfa aralığından DEĞİL): operatörün
 * sürüklediği kutu SORUNUN KENDİSİ, onu sayfanın geniş penceresine
 * yaymak seçimi anlamsızlaştırırdı. Bu, v0.9.1347'nin uyardığı
 * "exemplar'ın kendi dar penceresini ileri linke çivileme" tuzağı
 * DEĞİL: bir trace milisaniyeler sürer ve o pencere yanlışlıkla
 * taşınır; kutu ise operatörün AÇIKÇA çizdiği dakikalar-ölçekli bir
 * seçim ve pivotun bütün konusu o.
 *
 * service boşsa servis çipi HİÇ yazılmaz — "tüm servisler" bir değer
 * değil, yokluk.
 *
 * v0.9.1356 — `custom:` dizesi elle kuruluyordu; artık aile üreticisi
 * (tracesPivotHref → windowRangeParam) kuruyor, yani floor/ceil VE
 * kabul kuralı tek dosyada. Elle kurulan hâli kabul kuralını hiç
 * taşımıyordu: bozuk bir kutu, decodeRange'in reddedeceği bir token
 * basardı.
 */
export function heatmapTracesHref(box: {
  timeFromNs: number; timeToNs: number; lowDurMs: number; highDurMs: number;
}, service?: string): string {
  return tracesPivotHref({
    window: { fromNs: box.timeFromNs, toNs: box.timeToNs },
    service: service || undefined,
    minMs: Math.max(0, Math.floor(box.lowDurMs)),
    maxMs: Math.ceil(box.highDurMs),
    view: 'list',
    rootOnly: false,
  });
}
