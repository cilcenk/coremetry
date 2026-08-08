// stacking.ts — yığılmış ALAN modunun SAF çekirdeği (v0.9.788).
//
// İki fonksiyon, sıfır çizim bağımlılığı. Yığma bir render ayrıntısı
// DEĞİL bir veri dönüşümüdür: kümülatif matris + hangi seri çiftinin
// arasının dolacağı. Saf kalırsa tabloyla test edilir, canvas istemez —
// ve chart primitifi tekelinin (corePanelContracts "tekeli" testi) dışına
// hiç çıkmaz. Bu dosya @grafana ailesinden HİÇBİR ŞEY import etmez;
// tek tip bağımlılığı uPlot'un AlignedData biçimi.
//
// TEK KURAL, TimeSeriesPanel.tsx:285-291'den birebir devralındı:
//
//     cum[i][j] = (cum[i-1][j] ?? 0) + (v ?? 0)
//
// yani bir katmandaki null 0 SAYILIR. Aksi hâlde tek bir eksik bucket
// üstündeki TÜM katmanları delip geçer, yığın o sütunda çöker. Ham
// matris (tooltip/lejant/CSV) bu kuraldan ETKİLENMEZ — orada null hâlâ
// "veri yok"tur; 0'a çevrilen yalnız ÇİZİM kopyasıdır.

import type { AlignedData } from 'uplot';

// Köprünün (framesToAligned) ürettiği somut çizim matrisi: [zaman,
// ...seriler]. AlignedData'nın TypedArray dalı bu hatta hiç görünmez, o
// yüzden girdi tipi daraltılır — dallanma yerine sözleşme.
export type SeriesMatrix = [number[], ...(number | null)[][]];

// stackData — ham matristen kümülatif (çizim) matrisi.
//
// hiddenIdx: 0-tabanlı GİZLİ seri indeksleri. Gizli katman toplama
// KATILMAZ (lejanttan bir katman kapatınca üstteki katmanlar gerçekten
// aşağı iner — Grafana davranışı). Gizli serinin satırı, altındaki
// koşan toplamın kopyasıdır: uPlot onu zaten çizmez (show:false) ama
// y-ölçeği hesabı ve bir sonraki config kurulumuna kadarki tek kare
// için nötr kalması gerekir. Hiç görünür katman yoksa sıfır satırı.
export function stackData(data: SeriesMatrix, hiddenIdx: Set<number>): AlignedData {
  const t = data[0];
  const ys = data.slice(1) as (number | null)[][];
  const out: (number | null)[][] = [];
  let below: number[] | null = null;
  for (let i = 0; i < ys.length; i++) {
    if (hiddenIdx.has(i)) {
      out.push(below ? below.slice() : ys[i].map(() => 0));
      continue;
    }
    const cur = ys[i].map((v, j) => (below?.[j] ?? 0) + (v ?? 0));
    out.push(cur);
    below = cur;
  }
  return [t, ...out];
}

// stackBands — ardışık GÖRÜNÜR katmanların arasını dolduracak bant
// çiftleri. Seri indeksleri 1-TABANLI döner (uPlot'ta 0 = x ekseni).
//
// Gizli seriler zincirden düşer: [A gizli, B, C] için tek bant B↔C olur,
// A'ya bağlı bir bant üretilmez. Bu şart — uPlot bir bandın alt kenarı
// gizliyse (`lowerEdge.show`) kırpmayı iptal edip dolguyu tabana kadar
// indirir, yani hayalet bir katman çizerdi.
//
// Dolgu RENGİ burada YOK ve bilerek yok: bant fill'i boş bırakılınca
// uPlot üst serinin kendi dolgusuna düşer, böylece katman rengi tek
// kanaldan (seri rolü → token) gelir ve burada ikinci bir palet doğmaz.
export function stackBands(
  seriesCount: number,
  hiddenIdx: Set<number>,
): { series: [number, number] }[] {
  const vis: number[] = [];
  for (let i = 0; i < seriesCount; i++) if (!hiddenIdx.has(i)) vis.push(i);
  const out: { series: [number, number] }[] = [];
  for (let k = 1; k < vis.length; k++) {
    out.push({ series: [vis[k] + 1, vis[k - 1] + 1] });
  }
  return out;
}
