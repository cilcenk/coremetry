import { describe, it, expect } from 'vitest';
import { readState, type ReadState } from './readState';

// v0.9.858 — UX denetimi K6: ~12 yüzey API HATASINI "veri yok" olarak
// sunuyordu, bazıları kurulum yönergesiyle birlikte.
//
// En pahalı üçü:
//   • /services: `/api/services` hatası "No services yet — Point your OTLP
//     exporter…" basıyordu. Backend arızası INSTRUMENTATION eksikliği gibi
//     sunuluyor, operatör saatlerce collector tarafında arıyordu.
//   • /traces aggregate: sorgu hatası BOŞ EKRAN (spinner yok, mesaj yok).
//   • /problems exception listesi: sorgu hatası BOŞ EKRAN — "exception yok,
//     temiziz" diye okunuyordu. Sinyal kaybının en pahalı yeri.
//
// Ortak kalıp tek bir yazım şekliydi:
//     {data !== undefined && (!data || data.length === 0) && <Empty/>}
// `!data` null için de DOĞRU olduğundan hata boş dala düşüyordu.
//
// Bu testler sınıflandırmayı pinler; asıl korunan özellik: null ASLA 'empty'
// dönmez.

describe('readState — K6 hata→boş ezme sınıfı', () => {
  const cases: Array<[string, { length: number } | null | undefined, ReadState]> = [
    ['undefined = henüz yükleniyor', undefined, 'loading'],
    ['null = okuma BAŞARISIZ',       null,      'error'],
    ['boş dizi = gerçekten veri yok', [],       'empty'],
    ['dolu dizi = hazır',            [1, 2],    'ready'],
    ['tek elemanlı dizi = hazır',    [0],       'ready'],
  ];
  for (const [name, input, want] of cases) {
    it(name, () => expect(readState(input as never)).toBe(want));
  }

  it('null ASLA empty değildir — bug\'ın tam kendisi', () => {
    // Eski guard'ın şekli: `!data || data.length === 0`. null için true
    // döndüğü an sayfa "veri yok" basıyordu.
    expect(readState(null)).not.toBe('empty');
    expect(readState(null)).not.toBe('ready');
  });

  it('undefined ile null AYRIŞIR — spinner ile hata aynı şey değil', () => {
    // İkisini birleştiren yüzeyler ya sonsuz spinner ya sessiz boş ekran
    // veriyordu (/traces aggregate, /problems).
    expect(readState(undefined)).not.toBe(readState(null));
  });

  it('dört durum birbirini dışlar — her girdi tam bir dala düşer', () => {
    // Render tarafında dört guard yan yana yazıldığından, iki dalın aynı anda
    // doğru olması çift render, hiçbirinin doğru olmaması BOŞ EKRAN demekti.
    const seen = new Set<ReadState>();
    for (const input of [undefined, null, [], [1]] as Array<{ length: number } | null | undefined>) {
      const s = readState(input as never);
      expect(seen.has(s), `${s} iki kez üretildi`).toBe(false);
      seen.add(s);
    }
    expect(seen.size).toBe(4);
  });

  it('length taşıyan her şeyi kabul eder (dizi, string-benzeri zarflar)', () => {
    expect(readState({ length: 0 })).toBe('empty');
    expect(readState({ length: 3 })).toBe('ready');
  });
});
