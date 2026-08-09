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

// ── v0.9.808 — YIĞILMIŞ ÇUBUK: çizim sırası ────────────────────────────
//
// Yığılmış ALAN'da katmanlar bantlarla dolar, çizim sırası önemsizdir.
// Yığılmış ÇUBUK'ta değildir ve sebebi uPlot'un bars path builder'ında:
// her çubuk TABANDAN (y=0) kümülatif değere kadar çizilir. Kümülatif
// matriste en ÜST katmanın çubuğu en uzundur; seriler mantıksal sırada
// (alttan üste) çizilirse her katman bir öncekini tamamen ÖRTER ve
// panelde yalnız en üst katmanın rengi kalır — yığın görünmez olur.
//
// Çözüm: iç çizim sırasını TERS çevirmek. En uzun kümülatif ÖNCE çizilir,
// üstüne gitgide kısalan çubuklar biner; ekranda kalan görüntü doğru
// katmanlanmış yığındır. Dolgunun OPAK olması şart (CorePanel: stacked-bars
// fillOpacity 100) — yarı saydam bir çubuk altındaki daha uzun çubuğu
// gösterir ve renkler karışır.
//
// Ters çevrilen YALNIZ çizim sırasıdır: lejant, tooltip, renk ve istatistik
// MANTIKSAL sırada kalır. Bu modül iki tabloyu birlikte üretir, çünkü
// ikisini ayrı yerlerde türetmek klasik "bir gün kayan hizalama" hatasıdır.

// seriesDrawOrder — çizim pozisyonu → MANTIKSAL seri indeksi.
// reversed=false ise kimlik ([0,1,…,n-1]); yani yığılmış-çubuk DIŞINDAKİ
// her mark bugünkü davranışta kalır ve çağıranın ikinci bir kod yolu
// tutmasına gerek olmaz.
export function seriesDrawOrder(count: number, reversed: boolean): number[] {
  const out: number[] = [];
  for (let i = 0; i < count; i++) out.push(reversed ? count - 1 - i : i);
  return out;
}

// drawPosOf — TERS tablo: mantıksal seri indeksi → çizim pozisyonu.
// uPlot'un series[] dizisine (1-tabanlı) erişen her yol bundan geçer:
// görünürlük setSeries'i, tooltip'in cursor.idxs okuması, odak stilleri.
export function drawPosOf(order: readonly number[]): number[] {
  const out = new Array<number>(order.length);
  order.forEach((logical, pos) => { out[logical] = pos; });
  return out;
}

// ── v0.9.850 — YIĞIN AİLESİ: KATMAN SIRASI ─────────────────────────────────
//
// Bu, yukarıdaki "çizim sırası"ndan BAŞKA bir sıradır. Orası uPlot'un bars
// path builder'ının örtme sorunuydu (teknik); burası OKUNABİLİRLİK.
//
// Eski SVG motoru (DashboardViz, v0.9.844'te söküldü) yığılmış panellerde
// serileri TOPLAM BÜYÜKLÜĞE göre sıralayıp AĞIRI ALTA koyuyordu. CorePanel'e
// geçişte (v0.9.796/808) bu davranış taşınmadı: v2 motoru sorgunun döndürdüğü
// sırayı koruyor. Sonuç, ince bir katmanın tabanda, kalın bir katmanın onun
// üstünde durması — yığının alt kenarı zaten düz olduğu için ALTTAKİ katman
// en kolay okunandır ve oraya en anlamlı (en büyük) katman gelmelidir. Üstteki
// katmanlar zaten dalgalı bir tabana oturur, ince olanların yeri orasıdır.
//
// SIRALAMA YALNIZ YIĞIN AİLESİNDE. line/bars/area'da katman kavramı yoktur;
// orada sıra lejant sırasıdır ve onu değiştirmek operatörün kurduğu düzeni
// (A, B, C) sebepsiz bozardı.
//
// RENKLER ETKİLENMEZ: CorePanel seri rengini ADDAN türetiyor
// (seriesRoleColor(name, role)), indeksten değil — aynı seri sıradan bağımsız
// aynı rengi alır. Sıralama indeks-tabanlı bir palete dokunsaydı, panelin
// renkleri her veri tazelemesinde zıplardı.

// seriesMagnitude — bir katmanın toplam büyüklüğü: Σ|değer|.
//
// MUTLAK DEĞER: negatif üretebilen bir seri (formül, delta metriği) toplamda
// sıfıra yaklaşır ve "hiç yok" gibi sıralanırdı — oysa ekranda kapladığı alan
// gerçektir. null/NaN atlanır (ölçülmemiş bir bucket sıfır DEĞİLDİR).
export function seriesMagnitude(
  points: ReadonlyArray<{ value: number | null | undefined }>,
): number {
  let sum = 0;
  for (const p of points) {
    const v = p.value;
    if (v == null || !isFinite(v)) continue;
    sum += Math.abs(v);
  }
  return sum;
}

// stackItemOrder — mantıksal item indeksleri, AĞIR ÖNCE (= alt katman).
//
// stacked=false → KİMLİK (aynı dizi bile ayrılmaz), yani yığın dışındaki
// markların kod yolu bayt-bayt bugünküdür.
//
// SIRALAMA KARARLI: eşit büyüklükteki katmanlar girdi sırasını korur (JS
// sort ES2019'dan beri stable). Kararsız olsaydı iki eşit seri her poll'de
// yer değiştirir, panel sebepsiz titrerdi.
export function stackItemOrder(mags: readonly number[], stacked: boolean): number[] {
  const idx = mags.map((_, i) => i);
  if (!stacked) return idx;
  return idx.sort((a, b) => mags[b] - mags[a]);
}

// reorderSeries — matris satırlarını çizim sırasına dizer. Zaman satırı
// (index 0) yerinde kalır; yalnız seri satırları taşınır. Kimlik sırasında
// GİRDİ AYNEN döner (yeni dizi bile ayrılmaz): non-stacked-bars yolunda
// poll başına gereksiz bir kopya çıkmasın — o matris uPlot'a giden sıcak
// veridir ve kimliği config karşılaştırmasına dokunur.
export function reorderSeries(data: AlignedData, order: readonly number[]): AlignedData {
  let identity = true;
  for (let i = 0; i < order.length; i++) if (order[i] !== i) { identity = false; break; }
  if (identity) return data;
  const ys = data.slice(1);
  return [data[0], ...order.map(i => ys[i])] as AlignedData;
}
