import { describe, it, expect } from 'vitest';
import { nodeWorkView, makeBaseline, imbalanceOf, worstShardImbalance, type NodeWorkRaw } from './chNodeWork';

// v0.9.543 — node iş dağılımı panelinin saf çekirdeği.
//
// Bu testlerin varlık sebebi: panel CANLI bir prod soruşturmasını
// besliyor ve yanlış bir sayı, yanlış bir dalı canlı tutar ya da
// doğru dalı öldürür. Üç yanıltma sınıfı ayrı ayrı pinli — üçü de
// SESSİZ (hata yok, sayı var, sonuç yanlış).

const NS = 1e6; // ms → ns
const node = (o: Partial<NodeWorkRaw> & { host: string }): NodeWorkRaw => ({
  shard: 1, replica: 1, uptimeS: 1000,
  cpuMicros: 0, mergeMillis: 0, insertedRows: 0, partFetches: 0,
  selectedBytes: 0, mergesLaunched: 0, ...o,
});

describe('nodeWorkView — taban ve fark', () => {
  it('taban yokken hiçbir şey ölçülmez (sıfır BASMAZ)', () => {
    const raw = [node({ host: 'a', cpuMicros: 5e6 })];
    const v = nodeWorkView(raw, null, 1000 * NS);
    expect(v.rows[0].cpuCores).toBeNull();
    expect(v.measurable).toBe(false);
    expect(v.elapsedMs).toBe(0);
  });

  it('fark çekirdeğe çevrilir', () => {
    const t0 = 1000 * NS, t1 = t0 + 10_000 * NS;   // 10 sn
    const base = makeBaseline([node({ host: 'a', cpuMicros: 0 })], t0);
    // 10 sn'de 20e6 µs CPU = 20 sn CPU = 2 çekirdek
    const v = nodeWorkView([node({ host: 'a', cpuMicros: 20e6 })], base, t1);
    expect(v.rows[0].cpuCores).toBeCloseTo(2, 5);
    expect(v.elapsedMs).toBe(10_000);
    expect(v.measurable).toBe(true);
  });

  it('süre SUNUCUNUN generatedAt farkından gelir — istemci saatinden değil', () => {
    // Gövde bayat servis edilirse (30s TTL + staleFactor) generatedAt
    // AYNI kalır; o hâlde geçen süre 0'dır ve hız kolonları
    // güncellenmemeli. İstemci duvar saatiyle hesaplasaydık "CPU 0.00
    // çekirdek" görünür, node boşta sanılırdı.
    const t0 = 1000 * NS;
    const base = makeBaseline([node({ host: 'a', cpuMicros: 0 })], t0);
    const v = nodeWorkView([node({ host: 'a', cpuMicros: 20e6 })], base, t0);
    expect(v.rows[0].cpuCores).toBeNull();
  });
});

// TUZAK 1 — restart'ı 0 sanmak.
describe('restart tespiti', () => {
  it('uptime geriye giderse ölçüm yok, satır işaretli', () => {
    const t0 = 1000 * NS, t1 = t0 + 10_000 * NS;
    const base = makeBaseline([node({ host: 'a', uptimeS: 5000, cpuMicros: 9e9 })], t0);
    const v = nodeWorkView([node({ host: 'a', uptimeS: 12, cpuMicros: 1e6 })], base, t1);
    expect(v.rows[0].restarted).toBe(true);
    expect(v.rows[0].cpuCores).toBeNull();
  });

  it('uptime okunamasa da sayaç DÜŞÜŞÜ restart kanıtıdır', () => {
    const t0 = 1000 * NS, t1 = t0 + 10_000 * NS;
    const base = makeBaseline([node({ host: 'a', uptimeS: 0, cpuMicros: 9e9 })], t0);
    const v = nodeWorkView([node({ host: 'a', uptimeS: 0, cpuMicros: 1e6 })], base, t1);
    expect(v.rows[0].restarted).toBe(true);
  });

  it('restart eden node DENGESİZLİK hesabından çıkar — ortalamayı düşürüp diğerlerini şişirmesin', () => {
    const t0 = 1000 * NS, t1 = t0 + 10_000 * NS;
    const base = makeBaseline([
      node({ host: 'a', cpuMicros: 0 }), node({ host: 'b', cpuMicros: 0 }),
      node({ host: 'c', uptimeS: 5000, cpuMicros: 9e9 }),
    ], t0);
    const v = nodeWorkView([
      node({ host: 'a', cpuMicros: 10e6 }), node({ host: 'b', cpuMicros: 10e6 }),
      node({ host: 'c', uptimeS: 5, cpuMicros: 1e6 }),   // restart
    ], base, t1);
    // a ve b eşit → 1.0. c dahil edilseydi (0 olarak) oran 1.5 çıkardı.
    expect(v.cpuImbalance).toBe(1);
  });
});

// TUZAK 2 — ölçülemedi ≠ dengeli.
describe('imbalanceOf', () => {
  it('hepsi sıfırsa NULL — 1.0 "kusursuz dağılım" demektir, oysa ölçüm yok', () => {
    expect(imbalanceOf([0, 0, 0, 0])).toBeNull();
  });
  it('tek node kıyaslanamaz', () => {
    expect(imbalanceOf([5])).toBeNull();
    expect(imbalanceOf([])).toBeNull();
  });
  it('eşit dağılım 1.0', () => {
    expect(imbalanceOf([10, 10, 10, 10])).toBe(1);
  });
  it('operatörün gerçek vakası: 17.97 / ~6.5 ≈ 2 kat ortalama', () => {
    // 4 node, biri 2.76x diğerlerinin — max/mean.
    expect(imbalanceOf([17.97, 6.52, 6.54, 3.19])).toBeCloseTo(2.1, 1);
  });
});

// TUZAK 3 — shard'lar arası kıyas.
describe('worstShardImbalance', () => {
  const row = (host: string, shard: number, cpu: number | null) =>
    ({ host, shard, replica: 1, restarted: false,
       cpuCores: cpu, mergeThreads: null, insertedRows: null, partFetches: null });

  it('kıyas SHARD İÇİNDE yapılır — shard çarpıklığı node suçu gibi okunmaz', () => {
    // Shard 1 dengeli (10/10), shard 2 dengeli (40/40) ama shard'lar
    // arası 4x. Dört node üzerinden düz max/mean 1.6 verirdi ve
    // "dengesizlik var" derdi; oysa her shard kendi içinde kusursuz.
    const rows = [row('a', 1, 10), row('b', 1, 10), row('c', 2, 40), row('d', 2, 40)];
    expect(worstShardImbalance(rows, r => r.cpuCores)).toBe(1);
  });

  it('shard İÇİ çarpıklık yakalanır — operatörün gerçek deseni', () => {
    // Shard 1: bir replika yazıyor diğeri indiriyor (5.8x).
    // Shard 2: iki replika paylaşıyor (1.0x). En kötüsü raporlanır.
    const rows = [row('dbp01', 1, 17.9), row('dbp02', 1, 3.1),
                  row('dbp03', 2, 6.5), row('dbp04', 2, 6.5)];
    const imb = worstShardImbalance(rows, r => r.cpuCores);
    expect(imb).not.toBeNull();
    expect(imb!).toBeGreaterThan(1.5);
  });

  it('shard bilinmiyorsa (0) hepsi tek grup — kaba kıyas, UI söylemeli', () => {
    const rows = [row('a', 0, 10), row('b', 0, 30)];
    expect(worstShardImbalance(rows, r => r.cpuCores)).toBe(1.5);
  });

  it('ölçülemeyen değerler hesaba girmez', () => {
    const rows = [row('a', 1, null), row('b', 1, null)];
    expect(worstShardImbalance(rows, r => r.cpuCores)).toBeNull();
  });
});
