// legendMode — v0.9.541. Lejantın TABLO mu LİSTE mi çizileceğinin saf kararı.
//
// Operatör (2026-08-02, mockup C + otomatik seçim onayı): pod grafiklerinde
// lejant tam pod adlarıyla dikey tabloydu; 40 pod = 40 satır, grafik sayfanın
// çok altına iniyordu. Grafana'nın yoğunluğunu veren şey dolgu değil, YATAY
// lejant — mockup'ta 40 serilik kıyasla doğrulandı (dolgular üst üste binince
// alttaki seriler kayboluyor; dolgu bilinçli kapalı kalıyor).
//
// Neden eşik, neden hep-liste değil: Table modu Last/Min/Max/Avg taşır ve az
// serili grafikte o sayılar ASIL bilgidir (tek pod'a bakarken "şu an kaç,
// tepe neydi"). Kalabalıkta ise okunmaz hale gelir ve yerini yer kaplamasıyla
// öder. Yani mod bir tercih değil, seri sayısının fonksiyonu.
//
// Eşik 6: mockup'taki 6 serilik kart Table modunda sayılarıyla birlikte
// rahat okunuyordu; 8+'de sütunlar daralıp sayılar kırpılmaya başlıyor.
export const LEGEND_TABLE_MAX = 6;

export type LegendMode = 'table' | 'list';

export function legendMode(seriesCount: number): LegendMode {
  return seriesCount > LEGEND_TABLE_MAX ? 'list' : 'table';
}
