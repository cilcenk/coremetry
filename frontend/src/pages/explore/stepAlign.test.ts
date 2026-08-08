import { describe, it, expect } from 'vitest';
import {
  measureStepSec, resolveStepSec, alignFormulaLetters, fmtStep,
} from './stepAlign';
import type { SpanMetricSeries } from '@/lib/types';

// v0.9.809 — formül çözünürlük hizası.
//
// Korunan kusur: A 15s bucket'ta, B 60s bucket'ta gelirken A/B'nin
// hesaplanması. formulaSeries buckets'ları KESİŞİMLE eşlediği için sayı
// üretiliyordu — dörtte bir büyüklüğünde, makul görünen, kimsenin
// sorgulamadığı bir sayı. Boş panelden tehlikeli olan sınıf.

const ns = (sec: number) => sec * 1e9;
function ser(times: number[]): SpanMetricSeries[] {
  return [{ groupKey: [], points: times.map(t => ({ time: ns(t), value: 1 })) }];
}

describe('measureStepSec', () => {
  it('düzgün ızgara → adım', () => {
    expect(measureStepSec(ser([0, 15, 30, 45]))).toBe(15);
    expect(measureStepSec(ser([0, 60, 120]))).toBe(60);
  });

  it('BOŞLUKLU seride EN KÜÇÜK fark ızgaradır (katları değil)', () => {
    // 0, 15, (boşluk), 60, 75 → farklar 15, 45, 15 → ızgara 15.
    expect(measureStepSec(ser([0, 15, 60, 75]))).toBe(15);
  });

  it('çok serili: ızgara serilerin BİRLEŞİMİNDEN', () => {
    const s: SpanMetricSeries[] = [
      { groupKey: ['a'], points: [{ time: ns(0), value: 1 }] },              // tek nokta
      { groupKey: ['b'], points: [0, 30, 60].map(t => ({ time: ns(t), value: 1 })) },
    ];
    expect(measureStepSec(s)).toBe(30);
  });

  it('ölçülemez durumlar 0 döner — "bilinmiyor", "sıfır" değil', () => {
    expect(measureStepSec(undefined)).toBe(0);
    expect(measureStepSec([])).toBe(0);
    expect(measureStepSec(ser([42]))).toBe(0);
  });
});

describe('resolveStepSec', () => {
  it('sunucunun sözü ölçümü EZER', () => {
    // Resolver 10s tabanına kelepçelemiş olabilir ve dönen veri seyrek
    // olabilir; zarftaki değer otoritedir.
    expect(resolveStepSec(10, ser([0, 30, 60]))).toBe(10);
  });

  it('sunucu söylemiyorsa ölçüme düşer (span-metric / metric-query yolları)', () => {
    expect(resolveStepSec(undefined, ser([0, 60, 120]))).toBe(60);
    // Rolling deploy: eski pod 0/eksik gönderir → sessizce yalan değil, ölçüm.
    expect(resolveStepSec(0, ser([0, 60, 120]))).toBe(60);
  });
});

describe('alignFormulaLetters', () => {
  it('aynı çözünürlük → hizalı, not yok', () => {
    const r = alignFormulaLetters('A / B', { A: 15, B: 15 });
    expect(r.aligned).toBe(true);
    expect(r.droppedLetters).toEqual([]);
    expect(r.note).toBeNull();
  });

  it('🔴 uyuşmayan harf DÜŞER ve sebep yazılır', () => {
    const r = alignFormulaLetters('A / B', { A: 15, B: 60 });
    expect(r.aligned).toBe(false);
    expect(r.baseStepSec).toBe(15);
    expect(r.droppedLetters).toEqual(['B']);
    expect(r.note).toContain('B 1dk');
    expect(r.note).toContain('15s');
  });

  it('taban ÇOĞUNLUKTUR — tek sapan harf düşer, ikisi kalmaz', () => {
    const r = alignFormulaLetters('A + B + C', { A: 60, B: 60, C: 15 });
    expect(r.baseStepSec).toBe(60);
    expect(r.droppedLetters).toEqual(['C']);
  });

  it('eşitlikte İNCE ızgara taban (keyfî "ilk harf" seçimi yok)', () => {
    const r = alignFormulaLetters('A / B', { A: 60, B: 15 });
    expect(r.baseStepSec).toBe(15);
    expect(r.droppedLetters).toEqual(['A']);
  });

  it('BİLİNMEYEN step düşürmez — bilmemek uyuşmamak değildir', () => {
    // C ölçülemedi (tek nokta): formülü sebepsiz kapatmamalı.
    const r = alignFormulaLetters('A + B + C', { A: 30, B: 30, C: 0 });
    expect(r.aligned).toBe(true);
    expect(r.droppedLetters).toEqual([]);
  });

  it('tek bilinen harf → karşılaştıracak bir şey yok, hizalı', () => {
    expect(alignFormulaLetters('A * 2', { A: 15 }).aligned).toBe(true);
    expect(alignFormulaLetters('A / B', { A: 0, B: 0 }).aligned).toBe(true);
  });

  it('formülün REFERANS VERMEDİĞİ harfin adımı hesaba katılmaz', () => {
    const r = alignFormulaLetters('A * 2', { A: 15, B: 3600 });
    expect(r.aligned).toBe(true);
  });
});

describe('fmtStep', () => {
  it('her birim dalı ayrı yazılır (v0.6.36 birim-karışımı disiplini)', () => {
    expect(fmtStep(15)).toBe('15s');
    expect(fmtStep(90)).toBe('90s');   // dakikaya BÖLÜNMEZ → saniye kalır
    expect(fmtStep(60)).toBe('1dk');
    expect(fmtStep(300)).toBe('5dk');
    expect(fmtStep(3600)).toBe('1sa');
    expect(fmtStep(7200)).toBe('2sa');
    expect(fmtStep(0)).toBe('?');
    expect(fmtStep(-5)).toBe('?');
  });
});
