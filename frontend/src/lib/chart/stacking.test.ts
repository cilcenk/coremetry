import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  stackData, stackBands, seriesDrawOrder, drawPosOf, reorderSeries,
  seriesMagnitude, stackItemOrder,
  type SeriesMatrix,
} from './stacking';

// v0.9.788 — yığılmış alan modunun saf çekirdeği. Tablo testleri
// TimeSeriesPanel'in yaşayan davranışını (null→0, ardışık bant çifti)
// çivilerken, gizli-katman yeniden hesabı ESKİ motorda OLMAYAN yeni
// sözleşmedir: TSP'nin bundle'ı yalnız [prepared, mode]'a bağlıydı, yani
// bir katmanı kapatmak yığını yeniden hesaplamıyordu.

const t = [0, 60, 120];

describe('stackData', () => {
  it('düz toplam: her katman altındakinin üstüne biner', () => {
    const m: SeriesMatrix = [t, [1, 2, 3], [10, 20, 30]];
    expect(stackData(m, new Set())).toEqual([t, [1, 2, 3], [11, 22, 33]]);
  });

  it('null delikli seri: null 0 sayılır, üst katman DELİNMEZ', () => {
    // TSP:285-291 kuralı. null'ı null bıraksaydık ikinci katman t=60'ta
    // kopardı ve yığın o sütunda tabana çökerdi.
    const m: SeriesMatrix = [t, [1, null, 3], [10, 20, 30]];
    expect(stackData(m, new Set())).toEqual([t, [1, 0, 3], [11, 20, 33]]);
  });

  it('gizli seri toplama KATILMAZ — üsttekiler aşağı iner', () => {
    const m: SeriesMatrix = [t, [1, 2, 3], [10, 20, 30], [100, 200, 300]];
    // Ortadaki (idx 1) gizli: 3. katman doğrudan 1. katmanın üstüne.
    expect(stackData(m, new Set([1]))).toEqual([
      t, [1, 2, 3], [1, 2, 3], [101, 202, 303],
    ]);
  });

  it('en alttaki gizliyse koşan toplam sıfırdan başlar', () => {
    const m: SeriesMatrix = [t, [5, 5, 5], [10, 20, 30]];
    expect(stackData(m, new Set([0]))).toEqual([t, [0, 0, 0], [10, 20, 30]]);
  });

  it('hepsi gizliyse tüm satırlar sıfır — çizim boş, çökme yok', () => {
    const m: SeriesMatrix = [t, [5, 5, 5], [10, 20, 30]];
    expect(stackData(m, new Set([0, 1]))).toEqual([t, [0, 0, 0], [0, 0, 0]]);
  });

  it('tek seri: değerler AYNEN geçer (yığmanın nötr elemanı)', () => {
    const m: SeriesMatrix = [t, [1, null, 3]];
    expect(stackData(m, new Set())).toEqual([t, [1, 0, 3]]);
  });

  it('boş: serisiz matris yalnız zaman satırı döner', () => {
    const noSeries: SeriesMatrix = [t];
    const noPoints: SeriesMatrix = [[]];
    expect(stackData(noSeries, new Set())).toEqual([t]);
    expect(stackData(noPoints, new Set())).toEqual([[]]);
  });

  it('ham matris MUTASYONA UĞRAMAZ — tooltip kanalı ham kalır', () => {
    const raw: (number | null)[] = [1, null, 3];
    const m: SeriesMatrix = [t, raw, [10, 20, 30]];
    stackData(m, new Set());
    expect(raw).toEqual([1, null, 3]);
    expect(m[1]).toBe(raw);
  });
});

describe('stackBands', () => {
  it('ardışık çiftler, 1-tabanlı: {series:[k+2,k+1]}', () => {
    expect(stackBands(3, new Set())).toEqual([
      { series: [2, 1] }, { series: [3, 2] },
    ]);
  });

  it('gizli seri zincirden düşer — hayalet alt kenar üretilmez', () => {
    // [A gizli, B, C] → tek bant B(2)↔C(3).
    expect(stackBands(3, new Set([0]))).toEqual([{ series: [3, 2] }]);
    // [A, B gizli, C] → tek bant A(1)↔C(3).
    expect(stackBands(3, new Set([1]))).toEqual([{ series: [3, 1] }]);
  });

  it('tek seri / tek görünür seri → bant YOK (taban dolgusu serinin kendisi)', () => {
    expect(stackBands(1, new Set())).toEqual([]);
    expect(stackBands(3, new Set([0, 1]))).toEqual([]);
  });

  it('boş: sıfır seri → boş liste', () => {
    expect(stackBands(0, new Set())).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// v0.9.808 — YIĞILMIŞ ÇUBUK çizim sırası.
//
// Kusur sınıfı: kümülatif matriste her çubuk TABANDAN çizilir, yani en üst
// katman en uzundur. Mantıksal sırada çizilirse her katman bir öncekini
// tamamen örter — panelde tek renk kalır ve hata mesajı YOKTUR. Ters sıra
// bunu çözer, ama ters çevrilen YALNIZ çizimdir: lejant/tooltip/renk
// mantıksal sırada kalmalı. Aşağıdaki eşleme tablosu ikisini birlikte
// çiviliyor — biri diğerinden kayarsa operatör yanlış seriye bakar.
// ---------------------------------------------------------------------------
describe('seriesDrawOrder / drawPosOf — eşleme tablosu (3 seri)', () => {
  // Mantıksal sıra: 0=A (en alt katman), 1=B, 2=C (en üst katman).
  const names = ['A', 'B', 'C'];

  it('kimlik sırası: yığılmış-çubuk DIŞINDA hiçbir şey değişmez', () => {
    expect(seriesDrawOrder(3, false)).toEqual([0, 1, 2]);
    expect(drawPosOf(seriesDrawOrder(3, false))).toEqual([0, 1, 2]);
  });

  it('ters sıra: en uzun kümülatif (en üst katman) ÖNCE çizilir', () => {
    expect(seriesDrawOrder(3, true)).toEqual([2, 1, 0]);
  });

  it('ÇİZİM sırası C·B·A iken LEJANT sırası A·B·C kalır', () => {
    const order = seriesDrawOrder(3, true);
    // Çizim: pozisyon 0'da C, 1'de B, 2'de A.
    expect(order.map(i => names[i])).toEqual(['C', 'B', 'A']);
    // Lejant/istatistik ham `names` dizisini okur — dokunulmaz.
    expect(names).toEqual(['A', 'B', 'C']);
  });

  it('RENK eşleşmesi: her mantıksal seri kendi çizim pozisyonunu bulur', () => {
    const order = seriesDrawOrder(3, true);
    const pos = drawPosOf(order);
    // A(0)→pozisyon 2, B(1)→1, C(2)→0.
    expect(pos).toEqual([2, 1, 0]);
    // Tur kapanışı: pozisyondan mantıksal indekse geri dönüş kayıpsız.
    names.forEach((_, li) => expect(order[pos[li]]).toBe(li));
  });

  it('drawPosOf kimlik sırasında da tam tersi (kendi kendinin tersi)', () => {
    expect(drawPosOf([0, 1, 2])).toEqual([0, 1, 2]);
    expect(drawPosOf(drawPosOf([2, 1, 0]))).toEqual([2, 1, 0]);
  });

  it('sınırlar: 0 ve 1 seri', () => {
    expect(seriesDrawOrder(0, true)).toEqual([]);
    expect(seriesDrawOrder(1, true)).toEqual([0]);
    expect(drawPosOf([])).toEqual([]);
  });
});

describe('reorderSeries — çizim matrisi', () => {
  it('kimlik sırasında GİRDİ AYNEN döner (kopya bile yok)', () => {
    const m: SeriesMatrix = [t, [1, 2, 3], [10, 20, 30]];
    expect(reorderSeries(m, [0, 1])).toBe(m);
  });

  it('ters sırada satırlar taşınır, ZAMAN satırı yerinde kalır', () => {
    const m: SeriesMatrix = [t, [1, 2, 3], [10, 20, 30], [100, 200, 300]];
    expect(reorderSeries(m, [2, 1, 0])).toEqual([
      t, [100, 200, 300], [10, 20, 30], [1, 2, 3],
    ]);
  });

  it('yığma + ters sıra ZİNCİRİ: en uzun kümülatif ilk satırda', () => {
    // Ham katmanlar A=1, B=10, C=100 → kümülatif 1 / 11 / 111.
    const raw: SeriesMatrix = [t, [1, 1, 1], [10, 10, 10], [100, 100, 100]];
    const cum = stackData(raw, new Set()) as SeriesMatrix;
    expect(cum).toEqual([t, [1, 1, 1], [11, 11, 11], [111, 111, 111]]);
    const drawn = reorderSeries(cum, seriesDrawOrder(3, true));
    // İlk çizilen = 111 (en uzun); son çizilen = 1 (en kısa, en üstte kalır).
    expect(drawn).toEqual([t, [111, 111, 111], [11, 11, 11], [1, 1, 1]]);
  });

  it('gizli katman zinciri: yeniden hesap + ters sıra birlikte çalışır', () => {
    const raw: SeriesMatrix = [t, [1, 1, 1], [10, 10, 10], [100, 100, 100]];
    // Ortadaki (B) gizli → C doğrudan A'nın üstüne (1 / 1 / 101).
    const cum = stackData(raw, new Set([1])) as SeriesMatrix;
    expect(cum).toEqual([t, [1, 1, 1], [1, 1, 1], [101, 101, 101]]);
    expect(reorderSeries(cum, seriesDrawOrder(3, true))).toEqual([
      t, [101, 101, 101], [1, 1, 1], [1, 1, 1],
    ]);
  });

  it('ham matris MUTASYONA UĞRAMAZ — satır referansları paylaşılır', () => {
    const a = [1, 2, 3];
    const b = [10, 20, 30];
    const m: SeriesMatrix = [t, a, b];
    const out = reorderSeries(m, [1, 0]);
    expect(out[1]).toBe(b);
    expect(out[2]).toBe(a);
    expect(m).toEqual([t, [1, 2, 3], [10, 20, 30]]);
  });
});

// ---------------------------------------------------------------------------
// TEKEL KAPISI. Bu modül SAF kalmalı: chart primitifleri CorePanel.tsx'in
// tekelinde (corePanelContracts). Bir gün "kolaylık olsun" diye buraya bir
// builder importu düşerse yığma mantığı render katmanına yapışır ve tablo
// testi imkânsızlaşır — kapı bunu kaynakta durdurur.
// ---------------------------------------------------------------------------
describe('stacking.ts saflığı', () => {
  // Yorumlar düşer — kapı KODU tarar. (Bu dosyanın kendi başlığı zaten
  // "@grafana import etmez" diye yazıyor; ham metni taramak kendi
  // belgesine takılırdı.)
  const src = readFileSync(resolve(__dirname, './stacking.ts'), 'utf8')
    .replace(/\/\/.*$/gm, '');

  it('@grafana ailesinden HİÇBİR import yok', () => {
    expect(src).not.toMatch(/@grafana/);
  });

  it('chart primitifi adları kodda hiç geçmez', () => {
    expect(src).not.toMatch(/UPlotChart/);
    expect(src).not.toMatch(/UPlotConfigBuilder/);
  });
});

// ── v0.9.850 — KATMAN SIRASI (okunabilirlik) ────────────────────────────────
//
// Yukarıdaki seriesDrawOrder bir ÖRTME sorununu çözer (uPlot bars path
// builder). Bu ayrı bir sıradır: eski SVG motoru (DashboardViz) yığılmış
// panelleri toplam büyüklüğe göre sıralayıp AĞIRI ALTA koyuyordu, v2'ye
// geçişte taşınmadı. Yığının alt kenarı düz olduğu için alttaki katman en
// kolay okunandır; en büyük katman oraya gelmeli.

describe('seriesMagnitude', () => {
  const pts = (...vs: (number | null)[]) => vs.map(value => ({ value }));

  it('Σ|değer| — mutlak değer', () => {
    expect(seriesMagnitude(pts(1, 2, 3))).toBe(6);
  });

  it('NEGATİF seri sıfıra yaklaşmaz — ekranda kapladığı alan gerçek', () => {
    // Toplam 0 olurdu; mutlak değer olmasaydı bu seri "hiç yok" sıralanırdı.
    expect(seriesMagnitude(pts(-5, 5, -5, 5))).toBe(20);
  });

  it('null/NaN ATLANIR (ölçülmemiş bucket sıfır DEĞİLDİR)', () => {
    expect(seriesMagnitude(pts(3, null, 4))).toBe(7);
    expect(seriesMagnitude([{ value: NaN }, { value: 2 }])).toBe(2);
  });

  it('boş seri 0', () => {
    expect(seriesMagnitude([])).toBe(0);
  });
});

describe('stackItemOrder', () => {
  it('yığın: AĞIR ÖNCE (dizinin başı = alt katman)', () => {
    expect(stackItemOrder([3, 10, 7], true)).toEqual([1, 2, 0]);
  });

  it('yığın DIŞI: KİMLİK — line/bars/area lejant sırasını korur', () => {
    expect(stackItemOrder([3, 10, 7], false)).toEqual([0, 1, 2]);
  });

  it('eşit büyüklükte KARARLI — panel poll başına titremez', () => {
    expect(stackItemOrder([5, 5, 5, 5], true)).toEqual([0, 1, 2, 3]);
    // Kısmî eşitlik: 4'ler kendi aralarında girdi sırasını korur.
    expect(stackItemOrder([4, 9, 4], true)).toEqual([1, 0, 2]);
  });

  it('tek katman ve boş liste', () => {
    expect(stackItemOrder([7], true)).toEqual([0]);
    expect(stackItemOrder([], true)).toEqual([]);
  });

  it('sıfır büyüklükteki katmanlar en sona iner (ama düşmez)', () => {
    expect(stackItemOrder([0, 2, 0, 5], true)).toEqual([3, 1, 0, 2]);
  });
});

// v0.10.383 — dış skill denetimi A2: karşılaştırma hayaleti yığına
// katılmaz. Pod throughput compare açıkken ~2× okunuyordu.
describe('stackData/stackBands — ham (hayalet) seri yığın dışı', () => {
  const m: SeriesMatrix = [[1, 2], [1, 1], [2, 2], [10, 10]];
  it('ham seri toplama katılmaz ve AYNEN geçer', () => {
    const out = stackData(m, new Set(), new Set([2]));
    expect(out[1]).toEqual([1, 1]);
    expect(out[2]).toEqual([3, 3]);   // hayalet olmadan koşan toplam
    expect(out[3]).toEqual([10, 10]); // hayalet ham
  });
  it('ham seri banda girmez', () => {
    expect(stackBands(3, new Set(), new Set([2]))).toEqual([{ series: [2, 1] }]);
  });
  it('rawIdx verilmezse davranış aynen (geriye uyum)', () => {
    expect(stackData(m, new Set())[3]).toEqual([13, 13]);
  });
});
