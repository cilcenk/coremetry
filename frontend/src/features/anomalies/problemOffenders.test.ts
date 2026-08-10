// problemOffenders — v0.9.962 (UX denetimi G5 / Ö8).
//
// İki karar pinli:
//
//  1. AÇIK problemde pencerenin üst ucu BASAMAĞA yuvarlanır. Bileşen onu
//     `Date.now()` ile hesaplıyor; sorgu anahtarına ham girerse her
//     render yeni anahtar üretir (durmadan refetch) ve sunucudaki
//     serveCached anahtarı sınırsız değere dağılır — önbellek fiilen
//     kapanır. v0.5.184'ün render'da now() tıklatan sınıfı.
//  2. Sıralama TOPLAM ZAMAN. p99 sıralaması saniyede bir çağrılan bir
//     yönetim ucunu tepeye koyar ve servisin gecikmesini açıklamaz.

import { describe, it, expect } from 'vitest';
import { problemWindowNs, topOffenders, PROBLEM_WINDOW_RUNG_MS } from './problemOffenders';
import type { OperationSummary } from '@/lib/types';

const HOUR_NS = 60 * 60 * 1e9;
const TEN_MIN_NS = 10 * 60 * 1e9;

function op(name: string, spanCount: number, avgDurationMs: number): OperationSummary {
  return {
    name, spanCount, avgDurationMs,
    errorCount: 0, errorRate: 0,
    p50DurationMs: 0, p95DurationMs: 0, p99DurationMs: avgDurationMs * 3,
    apdex: 1, sparkline: [], errorsSparkline: [],
  } as unknown as OperationSummary;
}

describe('problemWindowNs', () => {
  const started = 1_700_000_000 * 1e9;

  it('KAPALI problem: resolvedAt aynen kullanılır (yuvarlama YOK)', () => {
    const resolved = started + 30 * 60 * 1e9;
    const w = problemWindowNs(started, resolved, Date.now());
    expect(w.fromNs).toBe(started - HOUR_NS);
    expect(w.toNs).toBe(resolved + TEN_MIN_NS);
  });

  it('AÇIK problem: üst uç basamağa YUKARI yuvarlanır', () => {
    const nowMs = 1_700_003_723_456; // basamak ortasında bir an
    const w = problemWindowNs(started, undefined, nowMs);
    const rung = Math.ceil(nowMs / PROBLEM_WINDOW_RUNG_MS) * PROBLEM_WINDOW_RUNG_MS;
    expect(w.toNs).toBe(rung * 1e6 + TEN_MIN_NS);
    // YUKARI: kapsam asla küçülmez, yani basamak veri kaybettirmez.
    expect(w.toNs).toBeGreaterThanOrEqual(nowMs * 1e6 + TEN_MIN_NS);
  });

  it('AÇIK problem: aynı basamaktaki her an AYNI anahtarı verir', () => {
    const base = 1_700_003_700_000; // tam basamak
    const a = problemWindowNs(started, undefined, base + 1);
    const b = problemWindowNs(started, undefined, base + 59_999);
    expect(a.toNs).toBe(b.toNs);
    // Bir sonraki basamak farklı olmalı — pencere donmuyor, sadece
    // dakikada bir ilerliyor.
    const c = problemWindowNs(started, undefined, base + 60_001);
    expect(c.toNs).toBeGreaterThan(a.toNs);
  });

  it('resolvedAt = 0 AÇIK sayılır (backend çözülmemişi 0 gönderir)', () => {
    const nowMs = 1_700_003_723_456;
    expect(problemWindowNs(started, 0, nowMs).toNs)
      .toBe(problemWindowNs(started, undefined, nowMs).toNs);
  });
});

describe('topOffenders', () => {
  it('TOPLAM ZAMANA göre sıralar — p99 değil', () => {
    const ops = [
      op('GET /health', 1, 900),        // toplam 900, p99 2700 (en yüksek p99)
      op('POST /pay', 1000, 40),        // toplam 40 000 ← gerçek suçlu
      op('GET /accounts', 500, 30),     // toplam 15 000
    ];
    const top = topOffenders(ops, 3).map(o => o.name);
    expect(top).toEqual(['POST /pay', 'GET /accounts', 'GET /health']);
  });

  it('ilk N ile sınırlar', () => {
    const ops = Array.from({ length: 20 }, (_, i) => op(`op-${i}`, 10 * (20 - i), 10));
    expect(topOffenders(ops, 5)).toHaveLength(5);
  });

  it('eşitlikte ada göre — iki render aynı sırayı verir', () => {
    const ops = [op('b', 10, 10), op('a', 10, 10), op('c', 10, 10)];
    expect(topOffenders(ops, 3).map(o => o.name)).toEqual(['a', 'b', 'c']);
  });

  it('girdiyi MUTASYONA UĞRATMAZ — çağıran listeyi başka yerde de çiziyor', () => {
    const ops = [op('b', 1, 1), op('a', 100, 100)];
    topOffenders(ops);
    expect(ops.map(o => o.name)).toEqual(['b', 'a']);
  });

  it('boş liste boş döner (çağıran Empty çizer)', () => {
    expect(topOffenders([], 5)).toEqual([]);
  });
});
