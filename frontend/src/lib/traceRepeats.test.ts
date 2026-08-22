// traceRepeats.test.ts — v0.9.1277.
//
// `traceRepeatGroups` Trace sayfasının N+1 uyarı çipinin TEK karar
// noktası: hangi desen gösterilir, hangisi susar, hangisi başa yazılır.
// Üçü de sessizce yanlış olabilecek türden — eşik bir sayı, kimlik iki
// alanın birleşimi, sıralama bir karşılaştırıcı.
//
// MUTASYON KANITI (2026-08-22): `.filter(g => g.count >= minCount)`
// satırı silindi → "eşik altındaki grup dönmüyor" ve "eşiğin TAM
// üstü/altı" vakaları kırmızı yandı; geri alındı, yeşil.
import { describe, it, expect } from 'vitest';
import { traceRepeatGroups, REPEAT_MIN_COUNT } from './traceRepeats';
import type { SpanRow } from './types';

// Minimal span fabrikası — traceRepeatGroups yalnız serviceName / name /
// attributes / startTime / endTime okuyor, gerisi tip tatmini.
function sp(service: string, name: string, durMs: number, i = 0): SpanRow {
  return {
    traceId: 't', spanId: `${service}-${name}-${i}`, parentSpanId: '',
    serviceName: service, name,
    startTime: 1_000_000_000 + i * 1_000_000,
    endTime: 1_000_000_000 + i * 1_000_000 + durMs * 1e6,
    statusCode: 'ok', kind: 'client', attributes: {},
  } as unknown as SpanRow;
}

function many(service: string, name: string, n: number, durMs = 10): SpanRow[] {
  return Array.from({ length: n }, (_, i) => sp(service, name, durMs, i));
}

describe('traceRepeatGroups', () => {
  it('boş dizi → boş sonuç', () => {
    expect(traceRepeatGroups([])).toEqual([]);
  });

  it('eşik ALTINDAKİ grup dönmüyor', () => {
    // 4 tekrar, varsayılan eşik 5 → hiç grup yok.
    expect(traceRepeatGroups(many('orders', 'SELECT items', 4))).toEqual([]);
  });

  it('eşik sınırı: n = minCount-1 sessiz, n = minCount görünür', () => {
    const below = traceRepeatGroups(many('orders', 'SELECT items', REPEAT_MIN_COUNT - 1));
    const at    = traceRepeatGroups(many('orders', 'SELECT items', REPEAT_MIN_COUNT));
    expect(below).toEqual([]);
    expect(at).toHaveLength(1);
    expect(at[0].count).toBe(REPEAT_MIN_COUNT);
  });

  it('aynı ad FARKLI serviste ayrı grup', () => {
    const spans = [
      ...many('orders',   'SELECT items', 6, 10),
      ...many('payments', 'SELECT items', 6, 10),
    ];
    const out = traceRepeatGroups(spans);
    expect(out).toHaveLength(2);
    expect(new Set(out.map(g => g.service))).toEqual(new Set(['orders', 'payments']));
    // Ad aynı olduğu için tek gruba KATLANMAMALI.
    expect(out.every(g => g.count === 6)).toBe(true);
  });

  it('aynı serviste farklı ad ayrı grup', () => {
    const spans = [
      ...many('orders', 'SELECT items', 5),
      ...many('orders', 'SELECT users', 5),
    ];
    expect(traceRepeatGroups(spans).map(g => g.name).sort())
      .toEqual(['SELECT items', 'SELECT users']);
  });

  it('sıralama: toplam süreye göre AZALAN (sayıya göre değil)', () => {
    const spans = [
      // 20 hızlı çağrı = 20ms toplam
      ...many('orders', 'SELECT items', 20, 1),
      // 6 yavaş çağrı = 600ms toplam — sayıca az, süre olarak baskın
      ...many('orders', 'SELECT orders', 6, 100),
    ];
    const out = traceRepeatGroups(spans);
    expect(out.map(g => g.name)).toEqual(['SELECT orders', 'SELECT items']);
    expect(out[0].totalMs).toBeCloseTo(600, 6);
    expect(out[1].totalMs).toBeCloseTo(20, 6);
  });

  it('eşit toplamda sayı, eşit sayıda ad ile deterministik kırılıyor', () => {
    const spans = [
      ...many('svc', 'bbb', 5, 10),
      ...many('svc', 'aaa', 5, 10),
    ];
    // İki koşuda da aynı sıra (Map ekleme sırasına DEĞİL, ada göre).
    expect(traceRepeatGroups(spans).map(g => g.name)).toEqual(['aaa', 'bbb']);
  });

  it('minCount açıkça geçilebiliyor', () => {
    const spans = many('orders', 'SELECT items', 3);
    expect(traceRepeatGroups(spans, 3)).toHaveLength(1);
    expect(traceRepeatGroups(spans, 4)).toHaveLength(0);
  });

  it('grup adı şelalenin gösterdiği adla AYNI (displaySpanName)', () => {
    // Jenerik gRPC adı — displaySpanName bunu rpc.* attribute'larından
    // zenginleştirir. Çip, şelaledeki satırla aynı dizeyi göstermeli,
    // yoksa "×5 grpc" yazıp operatör şelalede o adı bulamaz.
    const spans = Array.from({ length: 5 }, (_, i) => ({
      ...sp('checkout', 'grpc', 10, i),
      attributes: { 'rpc.service': 'PaymentSvc', 'rpc.method': 'Charge' },
    })) as SpanRow[];
    const out = traceRepeatGroups(spans);
    expect(out).toHaveLength(1);
    expect(out[0].name).toBe('PaymentSvc/Charge');
  });

  it('negatif süre 0 sayılıyor (bozuk saat toplamı aşağı çekmiyor)', () => {
    const bad = many('svc', 'x', 5, 10).map((s, i) =>
      i === 0 ? { ...s, endTime: s.startTime - 5_000_000 } : s);
    const out = traceRepeatGroups(bad);
    expect(out[0].totalMs).toBeCloseTo(40, 6);
  });
});
