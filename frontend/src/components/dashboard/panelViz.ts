// panelViz — dashboard MARKININ CorePanel karşılığını veren saf çekirdek
// (v0.9.796, v0.9.808'de tamamlandı).
//
// Dashboard beş mark tanır (PanelVizType) ve v0.9.808'den beri BEŞİ DE v2
// motorunda (CorePanel/@grafana/ui). Yığın ailesi son taşınandı:
//
//   • stacked-area → CorePanel'in 'stacked'ı. CorePanel'in yığını zaten
//     yığılmış ALAN'dır (v0.9.788), yani eşleme birebir.
//   • stacked-bar  → CorePanel'in 'stacked-bars'ı (v0.9.808). Aynı
//     kümülatif matris, DrawStyle.Bars markı ve TERS çizim sırası —
//     gerekçe lib/chart/stacking.ts'te (kümülatif çubuklar tabandan
//     çizilir, mantıksal sırada çizilirse birbirini örterler).
//
// v0.9.796'da ikisi bilinçli olarak eski SVG motorunda bırakılmıştı:
// o dilimin mockup onayı "bar/area" içindi ve yığılmış
// çubuğun v2'de karşılığı henüz yoktu. Yarısı yeni yarısı eski bir yığın
// ailesi bırakmamak için ikisi birlikte bekletildi, ikisi birlikte taşındı.
//
// isV2Viz KALKTI (v0.9.808): beş markın beşi de yeni motora gittiği için
// guard tautolojiye dönüyordu ve PanelRenderer'ın üç çağrı yerindeki eski
// SVG dalları ÖLÜ kalıyordu. Geriye-uyum şimi bırakmıyoruz (CLAUDE.md).
// v0.9.844 — o SVG motoru (DashboardViz) dosyasıyla birlikte SİLİNDİ:
// kalan tek tüketicisi motor kaçış kapısıydı, kapı da gitti.
//
// Karar SAF ve tek yerde: PanelRenderer'ın üç paneli (metric · spanmetric ·
// promql) aynı fonksiyonu çağırır, üç kopya dallanma değil — kopya sınıfının
// bedeli bu depoda ölçülü (v0.9.790'a kadar metric paneli mark'a hiç
// dallanmıyordu, çünkü dallanma üç yere elle yazılmıştı).

import type { PanelVizType } from '@/lib/types';

// CorePanel'in mark adları (viz union'ının dashboard'a düşen alt kümesi).
export type CoreViz = 'line' | 'bars' | 'area' | 'stacked' | 'stacked-bars';

// toCoreViz — dashboard adı → CorePanel adı. Adlar ÜÇ yerde ayrışıyor
// ('bar'→'bars', 'stacked-area'→'stacked', 'stacked-bar'→'stacked-bars');
// eşlemeyi elle yazmak yerine buradan geçirmek, altıncı bir mark
// eklendiğinde derleyicinin (exhaustive switch) uyarmasını sağlar. Tek
// harflik bir sapma CorePanel'in viz union'ında eşleşmez ve panel sessizce
// varsayılan 'line'a düşer — hata mesajı yok, yalnız yanlış grafik.
export function toCoreViz(viz: PanelVizType): CoreViz {
  switch (viz) {
    case 'bar':          return 'bars';
    case 'area':         return 'area';
    case 'line':         return 'line';
    case 'stacked-area': return 'stacked';
    case 'stacked-bar':  return 'stacked-bars';
  }
}
