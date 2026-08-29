// podPage.test.ts — v0.10.160 (pod detay sayfası, A anatomisi). Saf çekirdekler:
//   - ?spans= kodeği: errors (varsayılan, URL'de yazılmaz) | slow | all
//   - windowTotals: rate/error_rate/avg serilerinden pencere toplamları
//     (çağrı = Σ rate·Δt, hata % = ağırlıklı, ort. ms = rate-ağırlıklı)
//   - podTraceParams: /api/traces parametreleri (k8s.pod.name süzgeci,
//     hatalı/yavaş/tümü, sayfa 50, count=skip)
//   - joinSiblings: entity kardeşleri × clusterPods (topk 500) — eşleşmeyen
//     satırda faz/restart/cpu/mem BİLİNMİYOR ('—'), 0 değil
import { describe, it, expect } from 'vitest';
import { parseSpansParam, writeSpansParam, windowTotals, podTraceParams, joinSiblings, windowP95 } from './podPage';
import type { EntityRecord, ClusterPodRow } from '@/lib/types';

const pt = (t: number, v: number) => ({ time: t * 1e9, value: v });

describe('spans param', () => {
  it('varsayılan errors; bilinmeyen değer errors', () => {
    expect(parseSpansParam(null)).toBe('errors');
    expect(parseSpansParam('slow')).toBe('slow');
    expect(parseSpansParam('all')).toBe('all');
    expect(parseSpansParam('garbage')).toBe('errors');
  });
  it('varsayılan URL\'de YAZILMAZ, diğerleri yazılır; yabancı param korunur', () => {
    const p = new URLSearchParams('pod=x&range=1h');
    writeSpansParam(p, 'errors');
    expect(p.get('spans')).toBeNull();
    writeSpansParam(p, 'slow');
    expect(p.get('spans')).toBe('slow');
    expect(p.get('pod')).toBe('x');
    writeSpansParam(p, 'errors');
    expect(p.has('spans')).toBe(false);
  });
});

describe('windowTotals', () => {
  it('çağrı = Σ rate·Δt; hata % ağırlıklı; ort. ms rate-ağırlıklı', () => {
    const rate = [pt(0, 10), pt(60, 10), pt(120, 30)];          // req/s, 60 s adım
    const er = [pt(0, 0), pt(60, 50), pt(120, 0)];              // %
    const avg = [pt(0, 100), pt(60, 100), pt(120, 400)];        // ms
    const t = windowTotals(rate, er, avg);
    // Δt = 60: çağrı = (10+10+30)·60 = 3000; hata = 10·60·0.5 = 300 → %10
    expect(t.calls).toBe(3000);
    expect(t.errPct).toBeCloseTo(10, 5);
    // ort = (100·600 + 100·600 + 400·1800) / 3000 = 280
    expect(t.avgMs).toBeCloseTo(280, 5);
  });
  it('seyrek kovalar: adım en küçük pozitif Δt (boşluk çarpılmaz); zarf adımı verilirse o kazanır (inceleme #4)', () => {
    // 60 s adım, sonra 1 saat sessizlik: son kovadan önceki nokta 3600 s ile ÇARPILMAMALI
    const rate = [pt(0, 10), pt(60, 10), pt(3660, 10)];
    const t = windowTotals(rate, [], []);
    expect(t.calls).toBe(30 * 60);
    expect(windowTotals(rate, [], [], 15).calls).toBe(30 * 15);
  });
  it('avg/p95 ayrı batch: ZAMANA göre eşlenir, kova eksikse ağırlığa girmez (inceleme #3)', () => {
    const rate = [pt(0, 10), pt(60, 10), pt(120, 10)];
    const avg = [pt(0, 100), pt(120, 300)]; // 60 s kovası kafka-only → latency batch'inde yok
    expect(windowTotals(rate, [], avg).avgMs).toBeCloseTo(200, 5);
    expect(windowP95([pt(0, 100), pt(120, 300)], rate)).toBeCloseTo(200, 5);
    // indeksle eşleseydi: avg[1]=300 60 s kovasına düşer, 120 s kovası 0 → 133
  });
  it('tek nokta ya da boş seri → çağrı bilinmiyor (null), NaN yok', () => {
    expect(windowTotals([], [], [])).toEqual({ calls: null, errPct: null, avgMs: null });
    const t = windowTotals([pt(0, 5)], [pt(0, 0)], [pt(0, 10)]);
    expect(t.calls).toBeNull();
  });
  it('sıfır trafik → hata % ve ort. null (0 değil)', () => {
    const t = windowTotals([pt(0, 0), pt(60, 0)], [pt(0, 0), pt(60, 0)], [pt(0, 0), pt(60, 0)]);
    expect(t.calls).toBe(0);
    expect(t.errPct).toBeNull();
    expect(t.avgMs).toBeNull();
  });
  it('windowP95 = p95 serisinin rate-ağırlıklı ortalaması; veri yoksa null', () => {
    expect(windowP95([pt(0, 100), pt(60, 300)], [pt(0, 10), pt(60, 30)])).toBeCloseTo(250, 5);
    expect(windowP95([], [])).toBeNull();
    expect(windowP95([pt(0, 100)], [])).toBe(100);
  });
});

describe('podTraceParams', () => {
  const base = { pod: 'p-1', from: 1, to: 2, cluster: 'prod-eu-west', service: 'svc' };
  it('hatalı: filters k8s.pod.name + hasError, sayfa 50, count=skip', () => {
    const p = podTraceParams({ ...base, mode: 'errors', p95Ms: null }, 0)!;
    expect(JSON.parse(p.filters!)).toEqual([{ k: 'k8s.pod.name', op: '=', v: ['p-1'] }]);
    expect(p.hasError).toBe(true);
    expect(p.minMs).toBeUndefined();
    expect(p).toMatchObject({ limit: 50, offset: 0, count: 'skip', service: 'svc', cluster: 'prod-eu-west', from: 1, to: 2 });
  });
  it('yavaş: minMs = p95 (yuvarlanmış); p95 yoksa null döner (çip kapalı)', () => {
    const p = podTraceParams({ ...base, mode: 'slow', p95Ms: 812.4 }, 50);
    expect(p!.minMs).toBe(812);
    expect(p!.hasError).toBeUndefined();
    expect(p!.offset).toBe(50);
    expect(podTraceParams({ ...base, mode: 'slow', p95Ms: null }, 0)).toBeNull();
  });
  it('tümü: ne hasError ne minMs; servis/cluster boşsa anahtar yok', () => {
    const p = podTraceParams({ pod: 'p-1', from: 1, to: 2, cluster: '', service: '', mode: 'all', p95Ms: null }, 0);
    expect(p!.hasError).toBeUndefined();
    expect(p!.minMs).toBeUndefined();
    expect('service' in p!).toBe(false);
    expect('cluster' in p!).toBe(false);
  });
});

describe('joinSiblings', () => {
  const rec = (name: string, ns = 'payments'): EntityRecord => ({
    type: 'pod', clusterId: 'c1', id: `pod:c1/${ns}/${name}`, namespace: ns, name, source: 'thanos',
    validFrom: '2026-08-27T09:00:00Z', firstSeen: '2026-08-27T09:00:00Z', lastSeen: '2026-08-29T17:00:00Z',
  });
  const row = (pod: string, extra: Partial<ClusterPodRow> = {}): ClusterPodRow => ({ cluster: 'prod', namespace: 'payments', pod, cpuCores: 0.5, memBytes: 1e9, phase: 'Running', restarts: 0, ...extra });
  it('namespace+ad ile birleşir; listede olmayan pod BİLİNMİYOR', () => {
    const out = joinSiblings([rec('a'), rec('b')], [row('a', { restarts: 3 })]);
    expect(out[0]).toMatchObject({ name: 'a', known: true, restarts: 3, phase: 'Running' });
    expect(out[1]).toMatchObject({ name: 'b', known: false });
    expect(out[1].restarts).toBeNull();
    expect(out[1].cpuCores).toBeNull();
  });
  it('aynı ad farklı namespace eşleşmez; restartsUnknown → null', () => {
    const out = joinSiblings([rec('a', 'other')], [row('a', { restartsUnknown: true })]);
    expect(out[0].known).toBe(false);
    const out2 = joinSiblings([rec('a')], [row('a', { restartsUnknown: true })]);
    expect(out2[0].known).toBe(true);
    expect(out2[0].restarts).toBeNull();
  });
  it('null liste → hepsi bilinmiyor', () => {
    expect(joinSiblings([rec('a')], null)[0].known).toBe(false);
  });
});
