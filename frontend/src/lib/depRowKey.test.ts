import { describe, it, expect } from 'vitest';
import { depRowKey } from './depsTable';

// depRowKey regresyon testleri — v0.9.821.
//
// BUG: anahtar (system, cluster, görünen ad) idi; db.name taşımıyordu.
// Canlı veride oracle/oracle üzerinde 6, postgres üzerinde 9 ayrı
// veritabanı var — hepsi AYNI anahtarı üretiyordu, yani bir satıra
// tıklamak altı çekmeceyi birden açıyor ve ?row= linki hangi
// veritabanını kastettiğini taşımıyordu.

describe('depRowKey', () => {
  it('aynı instance üzerindeki iki veritabanı AYRI anahtar üretir', () => {
    const a = depRowKey({ system: 'oracle', instance: 'oracle', dbName: 'COREBANK' });
    const b = depRowKey({ system: 'oracle', instance: 'oracle', dbName: 'CARDS' });
    expect(a).not.toBe(b);
  });

  it('altı Oracle veritabanı → altı ayrı anahtar (canlı senaryo)', () => {
    const names = ['COREBANK', 'CARDS', 'DWH', 'AUDIT', 'LOANS', 'REWARDS'];
    const keys = new Set(names.map(n =>
      depRowKey({ system: 'oracle', instance: 'oracle', dbName: n })));
    expect(keys.size).toBe(6);
  });

  it('messaging satırı (db.name yok) kararlı kalır', () => {
    const k = depRowKey({ system: 'kafka', cluster: 'broker-1', destination: 'orders' });
    expect(k).toBe('kafka|broker-1|orders|');
    // Aynı destination farklı cluster'da → ayrı anahtar (v0.9.434 dersi).
    expect(k).not.toBe(depRowKey({ system: 'kafka', cluster: 'broker-2', destination: 'orders' }));
  });

  it('instance ile destination aynı alanı paylaşır (tip-silinmiş satır)', () => {
    expect(depRowKey({ system: 's', instance: 'x' }))
      .toBe(depRowKey({ system: 's', destination: 'x' }));
  });

  it('eksik alanlar boş string olur, undefined sızmaz', () => {
    expect(depRowKey({ system: 's' })).toBe('s|||');
  });

  it('db.name boşken anahtar tek bir sondaki ayraçla biter (eski davranış + alan)', () => {
    expect(depRowKey({ system: 'redis', instance: 'cache-1' })).toBe('redis||cache-1|');
  });
});
