import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import {
  foldTopN, foldedCount, foldNote, isOthersSeries, OTHERS_KEY, DEFAULT_MAX_SERIES,
} from './foldTopN';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.807 — "others" katlaması SAF modülde. Testler v1 gövdesinin
// (MultiLineChart.tsx:79-112) YAŞAYAN davranışını çiviliyor: bu modül bir
// yeniden yazım değil bir TAŞIMA, o yüzden her tablo satırı "eskiden ne
// oluyorduysa o" demek zorunda. Özellikle oran davranışı (%, ms, s →
// ORTALAMA) pinli: bir gün "toplam daha basit" diye tek kola indirilirse
// by-op latency panelinin "others" çizgisi tavana fırlar.

function s(key: string, ...vals: (number | null)[]): SpanMetricSeries {
  return {
    groupKey: [key],
    points: vals.map((v, i) => ({ time: i * 60e9, value: v as number })),
  };
}

describe('foldTopN — katlama sınırı', () => {
  it('N veya daha az seri: dizi AYNEN döner (aynı referans, kopya yok)', () => {
    const input = [s('a', 1), s('b', 2)];
    expect(foldTopN(input, 'rps', 8)).toBe(input);
    // Tam sınırda da katlama YOK (<=n).
    const eight = Array.from({ length: 8 }, (_, i) => s(`s${i}`, i + 1));
    expect(foldTopN(eight, 'rps', 8)).toBe(eight);
  });

  it('N+1 seri: üst N tutulur, kuyruk tek "others" serisine iner', () => {
    const nine = Array.from({ length: 9 }, (_, i) => s(`s${i}`, i + 1));
    const out = foldTopN(nine, 'rps', 8);
    expect(out).toHaveLength(9); // 8 kept + others
    expect(out[8].groupKey).toEqual([OTHERS_KEY]);
    // En küçük alanlı seri (s0, alan 1) kuyruğa düştü.
    expect(out.slice(0, 8).map(x => x.groupKey[0])).not.toContain('s0');
  });

  it('sıralama ALAN bazlı (Σ|değer|) — son nokta ya da ad değil', () => {
    // b'nin son noktası küçük ama toplam alanı en büyük.
    const out = foldTopN([s('a', 10, 0), s('b', 9, 9), s('c', 1, 1)], 'rps', 2);
    expect(out.map(x => x.groupKey[0])).toEqual(['b', 'a', OTHERS_KEY]);
  });

  it('negatif değerler MUTLAK alanla sıralanır', () => {
    const out = foldTopN([s('a', 1, 1), s('b', -50, -50)], 'rps', 1);
    expect(out[0].groupKey[0]).toBe('b');
  });
});

describe('foldTopN — kuyruk birleştirme', () => {
  it('toplanabilir birim (rps): kuyruk nokta bazında TOPLANIR', () => {
    const out = foldTopN([s('big', 100, 100), s('t1', 3, 4), s('t2', 5, 6)], 'rps', 1);
    expect(out[1].points).toEqual([
      { time: 0, value: 8 }, { time: 60e9, value: 10 },
    ]);
  });

  it('birimsiz panel de TOPLAR (bugünkü davranış)', () => {
    const out = foldTopN([s('big', 100), s('t1', 3), s('t2', 5)], undefined, 1);
    expect(out[1].points).toEqual([{ time: 0, value: 8 }]);
  });

  // ORAN DAVRANIŞI PİNİ — üç birimin ÜÇÜ de. Tek bir birimi test edip
  // "kol çalışıyor" demek bu depoda ispatlanmış bir tuzak
  // (feedback-unit-mixing-needs-both-branches).
  it.each(['%', 'ms', 's'])('oran birimi (%s): kuyruk ORTALANIR, toplanmaz', (unit) => {
    const out = foldTopN([s('big', 100), s('t1', 10), s('t2', 20)], unit, 1);
    expect(out[1].points).toEqual([{ time: 0, value: 15 }]);
  });

  it('birim büyük/küçük harf ve boşluktan bağımsız', () => {
    const out = foldTopN([s('big', 100), s('t1', 10), s('t2', 20)], ' MS ', 1);
    expect(out[1].points).toEqual([{ time: 0, value: 15 }]);
  });

  it('oran-DIŞI bir "req/s" birimi ortalamaya kaymaz', () => {
    const out = foldTopN([s('big', 100), s('t1', 10), s('t2', 20)], 'req/s', 1);
    expect(out[1].points).toEqual([{ time: 0, value: 30 }]);
  });
});

describe('foldTopN — delikli seriler', () => {
  it('null nokta toplama KATILMAZ ve ortalamanın bölenini düşürmez', () => {
    // t=0'da yalnız t2'nin değeri var → ortalama 20 (2'ye bölünmez).
    const out = foldTopN([s('big', 100, 100), s('t1', null, 10), s('t2', 20, 20)], 'ms', 1);
    expect(out[1].points).toEqual([
      { time: 0, value: 20 }, { time: 60e9, value: 15 },
    ]);
  });

  it('kuyruğun HİÇ noktası yoksa "others" boş noktalı çıkar (çökme yok)', () => {
    const empty: SpanMetricSeries = { groupKey: ['t1'], points: [] };
    const out = foldTopN([s('big', 100), empty], 'rps', 1);
    expect(out).toHaveLength(2);
    expect(out[1].groupKey).toEqual([OTHERS_KEY]);
    expect(out[1].points).toEqual([]);
  });

  it('birleşmiş noktalar ZAMANA göre sıralı döner (Map ekleme sırası değil)', () => {
    const late: SpanMetricSeries = {
      groupKey: ['t1'], points: [{ time: 120e9, value: 1 }, { time: 0, value: 2 }],
    };
    const out = foldTopN([s('big', 100), late], 'rps', 1);
    expect(out[1].points.map(p => p.time)).toEqual([0, 120e9]);
  });

  it('girdi serileri MUTASYONA UĞRAMAZ', () => {
    const a = s('a', 1, 2);
    const b = s('b', 3, 4);
    const before = JSON.stringify([a, b]);
    foldTopN([a, b], 'rps', 1);
    expect(JSON.stringify([a, b])).toBe(before);
  });
});

describe('isOthersSeries — katlanan seriyi tanıma', () => {
  it('yalnız TAM [others] tuple\'ı katlanmış sayılır', () => {
    expect(isOthersSeries({ groupKey: [OTHERS_KEY], points: [] })).toBe(true);
    expect(isOthersSeries({ groupKey: ['checkout'], points: [] })).toBe(false);
    // Gerçek bir grup tuple'ı "others" ile BAŞLIYOR olabilir — gri boyanmaz.
    expect(isOthersSeries({ groupKey: [OTHERS_KEY, 'checkout'], points: [] })).toBe(false);
    expect(isOthersSeries({ groupKey: [], points: [] })).toBe(false);
  });

  it('foldTopN\'in ürettiği seri tanınır, tuttukları tanınmaz', () => {
    const out = foldTopN([s('big', 100), s('t1', 1), s('t2', 2)], 'rps', 1);
    expect(out.map(isOthersSeries)).toEqual([false, true]);
  });
});

describe('foldedCount / foldNote — kırpma notu', () => {
  it('katlama yoksa sayı 0, not null', () => {
    expect(foldedCount(8, 8)).toBe(0);
    expect(foldNote(8, 8)).toBeNull();
    expect(foldNote(3, 8)).toBeNull();
  });

  it('katlanan seri sayısı = toplam − N', () => {
    expect(foldedCount(12, 8)).toBe(4);
    expect(foldNote(12, 8)).toBe('+4 seri katlandı (alan bazlı)');
  });

  it('varsayılan N 8 — MLC ile aynı', () => {
    expect(DEFAULT_MAX_SERIES).toBe(8);
    expect(foldedCount(10)).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// TEK KAYNAK KAPISI. Katlama BU modülden gelmeli; bir gün "hızlı olsun"
// diye MLC'ye ya da CorePanel yoluna ikinci bir kopya düşerse aynı panel
// yine farklı sayıda seri gösterir — kapı bunu kaynakta durdurur.
//
// v0.9.844 — sayı pini 2'den 1'e indi ve bu bir GEVŞEME DEĞİL: iki çağrı
// vardı çünkü iki motor vardı (v1 computeChartData + v2 items
// projeksiyonu). Eski motor sökülünce çağrı yeri de tekleşti; kapının
// koruduğu şey (kopya gövde YOK) aynen duruyor.
// ---------------------------------------------------------------------------
describe('tek kaynak — foldTopN kopyası YOK', () => {
  const mlc = readFileSync(
    resolve(__dirname, '../../components/MultiLineChart.tsx'), 'utf8',
  ).replace(/\/\/.*$/gm, '');

  it('MLC saf modülü import eder, kendi gövdesini taşımaz', () => {
    expect(mlc).toMatch(
      /import \{[^}]*foldTopN[^}]*\} from '@\/lib\/chart\/foldTopN'/);
    expect(mlc).not.toMatch(/function foldTopN\(/);
    expect(mlc).not.toMatch(/const OTHERS_KEY = 'others'/);
  });

  it('TEK çağrı yeri — katlama motora girmeden önce, bir kez', () => {
    expect((mlc.match(/foldTopN\(/g) ?? []).length).toBe(1);
  });

  it('katlanan seri "muted" rolüne + notu panele bağlanır', () => {
    expect(mlc).toMatch(/isOthersSeries\(s0\) \? 'muted'/);
    expect(mlc).toMatch(/note=\{foldNote\(/);
  });
});
