import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { stackData, stackBands, type SeriesMatrix } from './stacking';

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
